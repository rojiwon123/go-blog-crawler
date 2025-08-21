package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-blog-scraper/internal/models"

	network "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// TossScraper는 토스 기술 블로그 스크래핑을 위한 인터페이스입니다
type TossScraper interface {
	// Scrape는 1페이지부터 순회하며 lastPublishedAt 이후의 글만 수집합니다
	Scrape(ctx context.Context, lastPublishedAt time.Time) ([]models.BlogPost, error)
	Close() error
}

// TossScraperConfig는 토스 기술 블로그 스크래핑 설정을 담습니다
type TossScraperConfig struct {
	BaseURL             string        // 기본 URL (예: https://toss.tech)
	Source              string        // 소스명 (예: toss)
	Title               string        // 스크래퍼 설명용 타이틀
	CategoryPaths       []string      // 카테고리 경로 목록 (예: ["data-ml", "tech", "product"])
	MaxPages            int           // 최대 페이지 수 (안전장치)
	Headless            bool          // 헤드리스 모드
	Timeout             time.Duration // 전체 스크래핑 타임아웃
	InitialDelay        time.Duration // 초기 대기 (봇 감지 우회용)
	ListItemSelector    string        // 목록 아이템 선택자 (비워두면 기본 추정 로직 사용)
	CardSelector        string        // 카드(아이템) 컨테이너 선택자
	TitleSelector       string        // 제목 선택자 (상대 선택자)
	URLSelector         string        // URL 선택자 (상대 선택자)
	DateSelector        string        // 발행일 선택자 (상대 선택자)
	AuthorSelector      string        // 작성자 선택자 (상대 선택자)
	DescriptionSelector string        // 설명 선택자 (상대 선택자)
	ThumbnailSelector   string        // 썸네일 선택자 (상대 선택자)
	MetaDateSelector    string        // 카드 내 메타 날짜 선택자
	MetaAuthorSelector  string        // 카드 내 메타 작성자 선택자
	MaxConcurrent       int           // 상세 페이지 동시 처리 최대 수
}

// DefaultTossConfig는 합리적인 기본값을 제공합니다
func DefaultTossConfig() TossScraperConfig {
	return TossScraperConfig{
		BaseURL:       "https://toss.tech",
		Source:        "toss",
		Title:         "토스 기술 블로그",
		CategoryPaths: []string{"data-ml", "tech", "product"},
		MaxPages:      30,
		Headless:      true,
		Timeout:       3 * time.Minute,
		InitialDelay:  0,
		// 제공된 타이포그래피 클래스에 맞춘 기본 선택자
		ListItemSelector:    "", // 기본 추정 로직 사용
		CardSelector:        "div.css-132j2b5.e143n5sn1 li",
		TitleSelector:       "span.typography.typography--h6",
		URLSelector:         "a[href*='/article']",
		DateSelector:        "time",
		AuthorSelector:      "[rel='author'], .author, .byline",
		DescriptionSelector: "span.typography.typography--p, .p-article-card__summary, .summary, p",
		ThumbnailSelector:   "div.css-eqn1ha.e1sck7qg3 > img",
		MetaDateSelector:    "span.typography.typography--small",
		MetaAuthorSelector:  "span.typography.typography--small",
		MaxConcurrent:       6,
	}
}

type baseTossScraper struct {
	ctx    context.Context
	cancel context.CancelFunc
	config TossScraperConfig
}

// NewTossScraper는 새로운 토스 스크래퍼를 생성합니다
func NewTossScraper(config TossScraperConfig) (TossScraper, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	return &baseTossScraper{ctx: ctx, cancel: cancel, config: config}, nil
}

func (t *baseTossScraper) getPageURLFor(category string, page int) string {
	// https://toss.tech/{category}?page=1
	sep := "?"
	base := strings.TrimRight(t.config.BaseURL, "/")
	path := strings.Trim(category, "/")
	url := base
	if path != "" {
		url = url + "/" + path
	}
	if strings.Contains(url, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%spage=%d", url, sep, page)
}

// Scrape는 페이지 1부터 증가시키며 lastPublishedAt 이후 글만 수집합니다
func (t *baseTossScraper) Scrape(ctx context.Context, lastPublishedAt time.Time) ([]models.BlogPost, error) {
	log.Printf("🚀 %s 스크래핑 시작", t.config.Title)
	log.Printf("📅 %s 이후 글만 수집", lastPublishedAt.Format("2006-01-02"))

	// 브라우저 할당 컨텍스트 (공유)
	opts := t.getChromeOptions()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	categories := t.config.CategoryPaths
	if len(categories) == 0 {
		categories = []string{""}
	}

	type catResult struct {
		posts []models.BlogPost
		err   error
	}
	results := make(chan catResult, len(categories))

	for _, category := range categories {
		category := category
		go func() {
			// 카테고리별 독립 탭 컨텍스트
			taskCtx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
			defer cancel()
			if t.config.Timeout > 0 {
				var toCancel context.CancelFunc
				taskCtx, toCancel = context.WithTimeout(taskCtx, t.config.Timeout)
				defer toCancel()
			}
			// 네트워크 UA/헤더 설정 및 간단 스텔스 처리
			_ = chromedp.Run(taskCtx,
				network.Enable(),
				network.SetExtraHTTPHeaders(network.Headers{
					"Accept-Language": "ko-KR,ko;q=0.9,en-US;q=0.8,en;q=0.7",
				}),
			)
			_ = chromedp.Run(taskCtx, chromedp.Evaluate(`Object.defineProperty(navigator, 'webdriver', { get: () => undefined });`, nil))
			_ = chromedp.Run(taskCtx, chromedp.Evaluate(`try { delete window.chrome; } catch(e) {}`, nil))
			_ = chromedp.Run(taskCtx, chromedp.Evaluate(`Object.defineProperty(navigator, 'plugins', { get: () => [1,2,3] });`, nil))
			_ = chromedp.Run(taskCtx, chromedp.Evaluate(`Object.defineProperty(navigator, 'languages', { get: () => ['ko-KR','ko','en-US','en'] });`, nil))
			posts, err := t.scrapeCategory(taskCtx, category, lastPublishedAt)
			results <- catResult{posts: posts, err: err}
		}()
	}

	var merged []models.BlogPost
	seen := make(map[string]struct{})
	var firstErr error
	for i := 0; i < len(categories); i++ {
		r := <-results
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		for _, p := range r.posts {
			if _, ok := seen[p.URL]; ok {
				continue
			}
			seen[p.URL] = struct{}{}
			merged = append(merged, p)
		}
	}
	if firstErr != nil && len(merged) == 0 {
		return nil, firstErr
	}
	// 1차: 불완전한 항목 선별 후 상세 보강 (불완전 항목만)
	incomplete := make([]models.BlogPost, 0, len(merged))
	incompleteIdx := make([]int, 0, len(merged))
	for i, p := range merged {
		if p.Title == "" || p.Author == "" || p.Description == "" || p.Thumbnail == "" {
			incomplete = append(incomplete, p)
			incompleteIdx = append(incompleteIdx, i)
		}
	}
	if len(incomplete) > 0 {
		enriched, err := t.enrichPostsParallel(allocCtx, incomplete)
		if err == nil {
			for k, idx := range incompleteIdx {
				merged[idx] = enriched[k]
			}
		} else {
			log.Printf("⚠️  선택 보강 실패: %v", err)
		}
	}
	// 최종 정리: 작성자 클린업
	for i := range merged {
		merged[i].Author = sanitizeAuthor(merged[i].Author)
	}
	return merged, nil
}

// scrapeCategory는 특정 카테고리의 페이지네이션을 파싱하여 수집합니다
func (t *baseTossScraper) scrapeCategory(taskCtx context.Context, category string, lastPublishedAt time.Time) ([]models.BlogPost, error) {
	catLabel := category
	if catLabel == "" {
		catLabel = "/"
	}
	log.Printf("📂 카테고리 '%s' 처리 시작", catLabel)

	maxPage, err := t.findMaxPage(taskCtx, category)
	if err != nil {
		log.Printf("⚠️  최대 페이지 파악 실패(1페이지로 대체): %v", err)
		maxPage = 1
	}
	maxPage = min(maxPage, t.maxPagesOrDefault())
	if maxPage <= 0 {
		return nil, nil
	}

	var collected []models.BlogPost
	seen := make(map[string]struct{})

	for page := 1; page <= maxPage; page++ {
		url := t.getPageURLFor(category, page)
		log.Printf("📄 [%s] 페이지 %d/%d 로드: %s", catLabel, page, maxPage, url)

		if err := chromedp.Run(taskCtx, chromedp.Navigate(url)); err != nil {
			return nil, fmt.Errorf("페이지 이동 실패: %v", err)
		}

		// 에러 페이지로 유입되면 중단
		var path string
		if err := chromedp.Run(taskCtx, chromedp.Evaluate(`location.pathname`, &path)); err == nil {
			if path == "/error" {
				log.Printf("🛑 [%s] 에러 페이지 감지, 중단", catLabel)
				break
			}
		}

		if t.config.InitialDelay > 0 && page == 1 {
			time.Sleep(t.config.InitialDelay)
		}

		waitCtx, cancelWait := context.WithTimeout(taskCtx, 10*time.Second)
		_ = chromedp.Run(waitCtx, chromedp.WaitReady("main", chromedp.ByQuery))
		_ = chromedp.Run(waitCtx, chromedp.WaitReady("ul.p-pagination__list", chromedp.ByQuery))
		if strings.TrimSpace(t.config.CardSelector) != "" {
			_ = chromedp.Run(waitCtx, chromedp.WaitReady(t.config.CardSelector, chromedp.ByQueryAll))
		} else {
			_ = chromedp.Run(waitCtx, chromedp.WaitReady("a[href]", chromedp.ByQueryAll))
		}
		cancelWait()

		// 컨테이너 내 썸네일 img의 src가 http(s)로 채워질 때까지 최대 3초 폴링 (이미지 로드 강제 없음)
		for i := 0; i < 30; i++ {
			var httpImgs int
			_ = chromedp.Run(taskCtx, chromedp.Evaluate(`(() => {
				const cont = document.querySelector('div.css-132j2b5.e143n5sn1');
				if (!cont) return 0;
				const imgs = Array.from(cont.querySelectorAll('li div.css-eqn1ha.e1sck7qg3 > img'));
				return imgs.filter(img => {
					const s = (img.getAttribute('src') || '').trim();
					return s.startsWith('http://') || s.startsWith('https://');
				}).length;
			})()`, &httpImgs))
			if httpImgs > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		// 간단 재시도: 기사 링크가 등장할 때까지 최대 8초 대기
		for i := 0; i < 8; i++ {
			var artCount int
			_ = chromedp.Run(taskCtx, chromedp.Evaluate(`document.querySelectorAll("a[href*='/article']").length`, &artCount))
			if artCount > 0 {
				break
			}
			time.Sleep(1 * time.Second)
		}

		// 디버그: 링크 개수 로깅
		var allLinks, articleLinks int
		_ = chromedp.Run(taskCtx, chromedp.Evaluate(`document.querySelectorAll("a[href]").length`, &allLinks))
		_ = chromedp.Run(taskCtx, chromedp.Evaluate(`Array.from(document.querySelectorAll("a[href*='/article']")).filter(a=>{try{const u=new URL(a.href, location.href);return u.host===location.host && u.pathname.indexOf('/article/')===0;}catch(_){return false;}}).length`, &articleLinks))
		log.Printf("🔗 [%s] a[href]=%d, a[href*='/article']=%d", catLabel, allLinks, articleLinks)

		items, err := t.extractItems(taskCtx)
		if err != nil {
			log.Printf("⚠️  [%s] 목록 추출 실패(계속): %v", catLabel, err)
			continue
		}

		// 디버그: 문서 텍스트/링크 수 확인
		var textLen, hrefCount int
		_ = chromedp.Run(taskCtx, chromedp.Evaluate(`(document.body && document.body.innerText ? document.body.innerText.length : 0)`, &textLen))
		_ = chromedp.Run(taskCtx, chromedp.Evaluate(`document.querySelectorAll('a[href]').length`, &hrefCount))
		log.Printf("🧪 [%s] textLen=%d hrefCount=%d", catLabel, textLen, hrefCount)

		log.Printf("🔎 [%s] 페이지 %d: 후보 %d개", catLabel, page, len(items))

		pageHasNewer := false
		for _, it := range items {
			log.Printf("⏱️  [%s] 후보: title=%q url=%q rawDate=%q", catLabel, it.Title, it.URL, it.Date)
			publishedAt, ok := parseTossDate(it.Date)
			if !ok {
				// Title에서 날짜 fallback 파싱
				publishedAt, ok = parseTossDate(it.Title)
			}
			if !ok {
				log.Printf("⚠️  [%s] 날짜 파싱 실패: raw=%q", catLabel, it.Date)
				continue
			}
			log.Printf("📅 [%s] 파싱됨: %s", catLabel, publishedAt.Format("2006-01-02"))
			if !publishedAt.After(lastPublishedAt) {
				log.Printf("↩︎ [%s] 기준일 이전이거나 동일: %s", catLabel, publishedAt.Format("2006-01-02"))
				continue
			}
			if it.URL == "" {
				log.Printf("⚠️  [%s] URL 없음 스킵", catLabel)
				continue
			}
			if strings.HasPrefix(it.URL, "/") {
				it.URL = strings.TrimRight(t.config.BaseURL, "/") + it.URL
			}
			if _, exists := seen[it.URL]; exists {
				continue
			}
			seen[it.URL] = struct{}{}
			pageHasNewer = true

			collected = append(collected, models.BlogPost{
				Title:       it.Title,
				Description: it.Description,
				Author:      it.Author,
				PublishedAt: publishedAt,
				Thumbnail:   it.Thumbnail,
				URL:         it.URL,
				Source:      t.config.Source,
			})
		}

		if !pageHasNewer {
			log.Printf("🛑 [%s] 기준일 이후 글 없음, 중단", catLabel)
			break
		}
	}

	log.Printf("✅ [%s] 수집 완료: %d개", catLabel, len(collected))
	return collected, nil
}

// findMaxPage는 ul.p-pagination__list를 파싱하여 마지막 페이지 번호를 반환합니다
func (t *baseTossScraper) findMaxPage(ctx context.Context, category string) (int, error) {
	url := t.getPageURLFor(category, 1)
	if err := chromedp.Run(ctx, chromedp.Navigate(url)); err != nil {
		return 0, fmt.Errorf("페이지 이동 실패: %w", err)
	}
	var path string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.pathname`, &path)); err == nil {
		if path == "/error" {
			return 0, nil
		}
	}
	var maxPage int
	js := `(() => {
		const ul = document.querySelector('ul.p-pagination__list');
		if (!ul) return 1;
		const nums = Array.from(ul.querySelectorAll('a, li'))
			.map(el => (el.textContent || '').trim())
			.filter(tx => /^\d+$/.test(tx))
			.map(tx => parseInt(tx, 10));
		if (!nums.length) return 1;
		return Math.max(...nums);
	})()`
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &maxPage)); err != nil {
		return 1, nil
	}
	if maxPage <= 0 {
		maxPage = 1
	}
	return maxPage, nil
}

// Close는 스크래핑을 종료합니다
func (t *baseTossScraper) Close() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

func (t *baseTossScraper) getChromeOptions() []chromedp.ExecAllocatorOption {
	return append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", t.config.Headless),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
	)
}

func (t *baseTossScraper) maxPagesOrDefault() int {
	if t.config.MaxPages > 0 {
		return t.config.MaxPages
	}
	return 30
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type extractedItem struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Date        string `json:"date"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
}

// extractItems는 현재 페이지에서 목록 요소들을 추출합니다
func (t *baseTossScraper) extractItems(ctx context.Context) ([]extractedItem, error) {
	var js string
	// 구성된 선택자가 있으면 우선 사용하고, 없으면 보수적인 기본 로직으로 수집합니다
	if strings.TrimSpace(t.config.ListItemSelector) != "" {
		js = fmt.Sprintf(`(() => {
			const wrap = (v) => (v == null ? "" : String(v).trim());
			const findDateFrom = (root) => {
				const t = root.querySelector('time');
				const raw = t ? (t.getAttribute('datetime') || t.textContent) : '';
				let s = wrap(raw);
				if (!s) s = wrap(root.textContent || '');
				const kr = s.match(/(\\d{4})\\s*년\\s*(\\d{1,2})\\s*월\\s*(\\d{1,2})\\s*일/);
				if (kr) return (kr[1] + '-' + ('0'+kr[2]).slice(-2) + '-' + ('0'+kr[3]).slice(-2));
				const ymd = s.match(/(\\d{4})[./-](\\d{1,2})[./-](\\d{1,2})/);
				if (ymd) return (ymd[1] + '-' + ('0'+ymd[2]).slice(-2) + '-' + ('0'+ymd[3]).slice(-2));
				return '';
			};
			const items = Array.from(document.querySelectorAll(%q));
			return items.map(el => {
				const pick = sel => sel ? el.querySelector(sel) : null;
				const titleEl = pick(%q) || el.querySelector("a");
				const urlEl = pick(%q) || el.querySelector("a");
				const dateEl = pick(%q) || el.querySelector("time");
				const authorEl = pick(%q);
				const descEl = pick(%q);
				const imgEl = pick(%q) || el.querySelector("img");
				return {
					title: wrap(titleEl ? titleEl.textContent : ""),
					url: urlEl ? (urlEl.href || "") : "",
					date: (dateEl ? wrap(dateEl.getAttribute('datetime') || dateEl.textContent) : '') || findDateFrom(el),
					author: wrap(authorEl ? authorEl.textContent : ""),
					description: wrap(descEl ? descEl.textContent : ""),
					thumbnail: wrap(imgEl ? (imgEl.getAttribute('src') || "") : "")
				};
			});
		})()`,
			t.config.ListItemSelector,
			t.config.TitleSelector,
			t.config.URLSelector,
			t.config.DateSelector,
			t.config.AuthorSelector,
			t.config.DescriptionSelector,
			t.config.ThumbnailSelector,
		)
	} else {
		// 카드 컨테이너 기준으로 필드 추출
		if strings.TrimSpace(t.config.CardSelector) != "" {
			js = fmt.Sprintf(`(() => {
				const wrap = (v) => (v == null ? "" : String(v).trim());
				const items = Array.from(document.querySelectorAll(%q));
				return items.map(root => {
					const pick = sel => sel ? root.querySelector(sel) : null;
					const titleEl = pick(%q);
					const anchorEl = pick(%q) || root.querySelector('a[href*="/article"]') || root.querySelector('a[href]');
					const metaEl = pick(%q);
					// 작성자는 메타 텍스트 분리로만 추출 (역할/직무 텍스트 혼입 방지)
					// const authorEl = pick(%q);
					const authorEl = null;
					const descEl = pick(%q);
					const title = wrap(titleEl ? titleEl.textContent : (anchorEl ? anchorEl.textContent : ''));
					let url = anchorEl ? (anchorEl.href || '') : '';
					// 절대 URL화 및 동일 호스트 /article/* 필터
					try {
						const u = new URL(url, location.href);
						if (u.host !== location.host || !u.pathname.startsWith('/article/')) { url = ''; } else { url = u.href; }
					} catch { url = ''; }
					// meta 텍스트에서 날짜 · 작성자 분리
					const metaText = wrap(metaEl ? metaEl.textContent : '');
					let date = '';
					let author = '';
					if (metaText) {
						let sep = ' · ';
						if (metaText.indexOf(' · ') === -1 && metaText.indexOf(' . ') !== -1) sep = ' . ';
						const parts = metaText.split(sep);
						if (parts.length >= 1) date = wrap(parts[0]);
						if (parts.length >= 2) author = wrap(parts[1]);
					}
					if (!date) {
						const t = root.querySelector(%q);
						date = wrap(t ? (t.getAttribute('datetime') || t.textContent) : '');
					}
					const description = wrap(descEl ? descEl.textContent : '');
					// 썸네일: 카드 내 첫 번째 http(s) img[src]
					let thumbnail = '';
					const imgs = root.querySelectorAll('img');
					for (const img of imgs) {
						const s = wrap(img.getAttribute('src') || '');
						if (!s) continue;
						if (s.startsWith('http://') || s.startsWith('https://')) { thumbnail = s; break; }
					}
					if (!thumbnail) {
						const ns = root.querySelector('noscript');
						if (ns && ns.innerHTML) { const tmp=document.createElement('div'); tmp.innerHTML = ns.innerHTML; const nsImg = tmp.querySelector('img'); if (nsImg) { const s = wrap(nsImg.getAttribute('src') || ''); if (s.startsWith('http') ) thumbnail = s; } }
					}
					return { title, url, date, author, description, thumbnail };
				});
			})()`,
				t.config.CardSelector,
				t.config.TitleSelector,
				t.config.URLSelector,
				t.config.MetaDateSelector,
				t.config.MetaAuthorSelector,
				t.config.DescriptionSelector,
				t.config.DateSelector,
			)
		} else {
			// 전역 링크 기반 추정 (폴백)
			js = `(() => {
				const wrap = (v) => (v == null ? "" : String(v).trim());
				const dateKR = /(\d{4})\s*년\s*(\d{1,2})\s*월\s*(\d{1,2})\s*일/;
				const dateAny = /(\d{4})[./-](\d{1,2})[./-](\d{1,2})/;
				const findDateFrom = (root) => {
					const t = root.querySelector('time');
					const raw = t ? (t.getAttribute('datetime') || t.textContent) : '';
					let s = wrap(raw);
					if (!s) s = wrap(root.textContent || '');
					const kr = s.match(dateKR);
					if (kr) return (kr[1] + '-' + ('0'+kr[2]).slice(-2) + '-' + ('0'+kr[3]).slice(-2));
					const ymd = s.match(dateAny);
					if (ymd) return (ymd[1] + '-' + ('0'+ymd[2]).slice(-2) + '-' + ('0'+ymd[3]).slice(-2));
					return '';
				};
				const links = Array.from(document.querySelectorAll("a[href*='/article']"));
				const uniq = new Map();
				for (const a of links) {
					try {
						const u = new URL(a.href, location.href);
						if (u.pathname.indexOf('/article/') === -1) continue;
						if (!uniq.has(u.href)) uniq.set(u.href, a);
					} catch (_) {}
				}
				const items = Array.from(uniq.values()).map(a => {
					const root = a.closest('article') || a.closest('li') || a.parentElement || a;
					const titleEl = root.querySelector("span.typography.typography--h6, h2, h3, .p-article-card__title, a[title]");
					const descEl = root.querySelector('span.typography.typography--p, .p-article-card__summary, .summary, p');
					const authorEl = root.querySelector("span.typography.typography--small, [rel='author'], .author, .byline");
					const dateEl = root.querySelector('span.typography.typography--small, time');
					const title = wrap(titleEl ? titleEl.textContent : a.textContent);
					const url = a.href;
					let date = wrap(dateEl ? (dateEl.getAttribute('datetime') || dateEl.textContent) : '');
					let author = wrap(authorEl ? authorEl.textContent : '');
					if (author && /\d{4}\s*년/.test(author)) {
						let meta = author;
						let sep = ' · ';
						if (meta.indexOf(' · ') === -1 && meta.indexOf(' . ') !== -1) sep = ' . ';
						const parts = meta.split(sep);
						if (parts.length >= 1) date = wrap(parts[0]);
						if (parts.length >= 2) author = wrap(parts[1]);
					}
					if (!date) date = findDateFrom(root);
					const description = wrap(descEl ? descEl.textContent : '');
					// 카드 내 첫 번째 http(s) img[src]
					let thumbnail = '';
					const imgs = root.querySelectorAll('img');
					for (const img of imgs) {
						const s = wrap(img.getAttribute('src') || '');
						if (!s) continue;
						if (s.startsWith('http://') || s.startsWith('https://')) { thumbnail = s; break; }
					}
					if (!thumbnail) {
						const ns = root.querySelector('noscript');
						if (ns && ns.innerHTML) { const tmp=document.createElement('div'); tmp.innerHTML = ns.innerHTML; const nsImg = tmp.querySelector('img'); if (nsImg) { const s = wrap(nsImg.getAttribute('src') || ''); if (s.startsWith('http')) thumbnail = s; } }
					}
					return { title, url, date, author, description, thumbnail };
				});
				return items;
			})()`
		}
	}

	var out []extractedItem
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &out)); err != nil {
		return nil, err
	}
	return out, nil
}

// enrichPostsParallel는 각 포스트 상세 페이지에서 제목/작성자/설명/썸네일을 정제합니다
func (t *baseTossScraper) enrichPostsParallel(parentCtx context.Context, posts []models.BlogPost) ([]models.BlogPost, error) {
	if len(posts) == 0 {
		return posts, nil
	}
	concurrency := t.config.MaxConcurrent
	if concurrency <= 0 {
		concurrency = 4
	}
	type job struct{ idx int }
	jobs := make(chan job)
	var wg sync.WaitGroup
	mu := &sync.Mutex{}
	out := make([]models.BlogPost, len(posts))
	copy(out, posts)

	worker := func() {
		defer wg.Done()
		ctx, cancel := chromedp.NewContext(parentCtx)
		defer cancel()
		// 네트워크 이벤트 활성화
		_ = chromedp.Run(ctx, network.Enable())
		for j := range jobs {
			p := out[j.idx]
			// 네트워크로부터 댓글 카운트 포착
			var netComments int
			var netMu sync.Mutex
			stopAt := time.Now().Add(8 * time.Second)
			chromedp.ListenTarget(ctx, func(ev interface{}) {
				// 이미 찾았으면 빠르게 반환
				netMu.Lock()
				already := netComments > 0
				netMu.Unlock()
				if already {
					return
				}
				switch e := ev.(type) {
				case *network.EventResponseReceived:
					if time.Now().After(stopAt) {
						return
					}
					resp := e.Response
					url := strings.ToLower(resp.URL)
					ct := strings.ToLower(resp.MimeType)
					if !strings.Contains(ct, "json") && !strings.Contains(url, "comment") {
						return
					}
					// JSON 응답만 파싱
					if strings.Contains(ct, "json") || strings.Contains(url, "comment") {
						body, err := network.GetResponseBody(e.RequestID).Do(ctx)
						if err != nil || len(body) == 0 {
							return
						}
						count := findCommentCountInJSONBytes(body)
						if count > 0 {
							netMu.Lock()
							netComments = count
							netMu.Unlock()
						}
					}
				}
			})
			if err := chromedp.Run(ctx, chromedp.Navigate(p.URL)); err != nil {
				continue
			}
			_ = chromedp.Run(ctx, chromedp.WaitReady("body", chromedp.ByQuery))
			// 약간의 렌더링 대기
			time.Sleep(1200 * time.Millisecond)
			// 댓글 탭/버튼이 있다면 클릭 시도 후 잠깐 대기
			var _ignored bool
			_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
				const els = Array.from(document.querySelectorAll('button, a, [role="button"], [onclick]'));
				for (const el of els) {
					const t = String((el.innerText||el.textContent||'')).trim();
					if (/댓글|comment/i.test(t)) {
						try { el.click(); } catch(e) {}
						return true;
					}
				}
				return false;
			})()`, &_ignored))
			time.Sleep(400 * time.Millisecond)
			var meta struct {
				Title, Author, Desc, Thumb string
				Comments                   int
			}
			js := `(() => {
				const wrap = (v) => (v == null ? "" : String(v).trim());
				const get = (sel) => { const el = document.querySelector(sel); return el ? wrap(el.textContent) : ''; };
				const getAttr = (sel, a) => { const el = document.querySelector(sel); return el ? wrap(el.getAttribute(a)) : ''; };
				let title = get('h1, .p-article__title, [class*="article"] h1') || getAttr("meta[property='og:title']", 'content');
				let author = get('[rel="author"], .author, [class*="author"], .byline a, .byline');
				let desc = getAttr("meta[name='description']", 'content');
				if (!desc) { const p=document.querySelector('article p, .p-article__content p'); if (p) desc = wrap(p.textContent); }
				let comments = 0;
				// 1) Next.js 데이터 검사
				(function extractFromNextData(){
					const tryExtract = (obj) => {
						let found = 0;
						const visit = (o) => {
							if (!o || typeof o !== 'object') return;
							for (const [k,v] of Object.entries(o)) {
								const key = String(k).toLowerCase();
								if (/(^|_)comment(s|count)?$/.test(key) || key.includes('comment')) {
									if (typeof v === 'number') { found = Math.max(found, v|0); }
									else if (Array.isArray(v)) { found = Math.max(found, v.length|0); }
									else if (typeof v === 'string') { const n = parseInt(v, 10); if (!isNaN(n)) found = Math.max(found, n); }
								}
								if (v && typeof v === 'object') visit(v);
							}
						}
						visit(obj);
						return found;
					};
					try {
						if (window.__NEXT_DATA__) {
							const n = tryExtract(window.__NEXT_DATA__);
							if (n > 0) { comments = n; return; }
						}
						const s = document.querySelector('#__NEXT_DATA__');
						if (s && s.textContent) {
							const data = JSON.parse(s.textContent);
							const n = tryExtract(data);
							if (n > 0) { comments = n; return; }
						}
					} catch(e) {}
				})();
				// 2) JSON-LD
				if (!comments) {
					try {
						const scripts = Array.from(document.querySelectorAll("script[type='application/ld+json']"));
						for (const s of scripts) {
							try {
								const data = JSON.parse(s.textContent || '{}');
								const arr = Array.isArray(data) ? data : [data];
								for (const obj of arr) {
									if (obj && typeof obj === 'object') {
										if (typeof obj.commentCount === 'number') { comments = obj.commentCount; break; }
										if (Array.isArray(obj.interactionStatistic)) {
											for (const st of obj.interactionStatistic) {
												if (st && st['@type'] && String(st['@type']).toLowerCase().includes('interactioncounter') && st.userInteractionCount && st.interactionType) {
													const t = String(st.interactionType).toLowerCase();
													if (t.includes('comment')) { comments = parseInt(st.userInteractionCount, 10) || 0; break; }
												}
											}
										}
									}
								}
							} catch(e) {}
							if (comments > 0) {}
						}
					} catch(e) {}
				}
				// 3) 메타/클래스/텍스트
				if (!comments) {
					const metas = Array.from(document.querySelectorAll('meta[name], meta[property]'));
					for (const m of metas) {
						const name = (m.getAttribute('name') || m.getAttribute('property') || '').toLowerCase();
						if (name.includes('comment')) {
							const v = (m.getAttribute('content') || '').trim();
							const n = parseInt(v, 10);
							if (!isNaN(n)) { comments = n; break; }
						}
					}
				}
				if (!comments) {
					const cand = document.querySelectorAll('[class*="comment" i], [href*="#comments" i], [id*="comments" i]');
					for (const el of cand) {
						const mm = (el.textContent || '').match(/(\d{1,4})/);
						if (mm) { comments = parseInt(mm[1], 10) || 0; if (comments>0) break; }
					}
				}
				if (!comments) {
					const text = document.body ? (document.body.innerText || '') : '';
					let m = text.match(/댓글\s*(\d{1,4})/);
					if (!m) m = text.match(/(\d{1,4})\s*comments?/i);
					if (m) { comments = parseInt(m[1], 10) || 0; }
				}
				// 상세 썸네일 (필요 시)
				let thumb = '';
				const imgs = document.querySelectorAll('article img, .p-article__content img, main img, figure img');
				for (const img of imgs) {
					const s = wrap(img.getAttribute('src') || '');
					if (!s) continue;
					if (s.startsWith('http://') || s.startsWith('https://')) { thumb = s; break; }
					try { const u = new URL(s, location.href); thumb = u.href; break; } catch(e){}
				}
				return { title, author, desc, Thumb: thumb, Comments: comments };
			})()`
			if err := chromedp.Run(ctx, chromedp.Evaluate(js, &meta)); err != nil {
				continue
			}
			// 댓글 수 폴링 (DOM/JSON-LD) + 네트워크 캡처 값 우선 적용
			if meta.Comments == 0 {
				for k := 0; k < 10; k++ {
					netMu.Lock()
					nc := netComments
					netMu.Unlock()
					if nc > 0 {
						meta.Comments = nc
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			if meta.Comments == 0 {
				// 마지막으로 DOM 재시도
				var tryMeta struct{ Comments int }
				poll := `(() => {
					let comments = 0;
					const text = document.body ? (document.body.innerText || '') : '';
					let m = text.match(/댓글\s*(\d{1,4})/);
					if (!m) m = text.match(/(\d{1,4})\s*comments?/i);
					if (m) { comments = parseInt(m[1], 10) || 0; }
					return { Comments: comments };
				})()`
				_ = chromedp.Run(ctx, chromedp.Evaluate(poll, &tryMeta))
				if tryMeta.Comments > 0 {
					meta.Comments = tryMeta.Comments
				}
			}
			mu.Lock()
			if out[j.idx].Title == "" && meta.Title != "" {
				out[j.idx].Title = meta.Title
			}
			if out[j.idx].Author == "" && meta.Author != "" {
				out[j.idx].Author = meta.Author
			}
			if out[j.idx].Description == "" && meta.Desc != "" {
				out[j.idx].Description = meta.Desc
			}
			if (out[j.idx].Thumbnail == "" || strings.HasPrefix(out[j.idx].Thumbnail, "data:")) && meta.Thumb != "" {
				out[j.idx].Thumbnail = meta.Thumb
			}
			if meta.Comments >= 0 {
				out[j.idx].Comments = meta.Comments
			}
			mu.Unlock()
		}
	}
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go worker()
	}
	for i := range posts {
		jobs <- job{idx: i}
	}
	close(jobs)
	wg.Wait()
	return out, nil
}

// findCommentCountInJSONBytes parses arbitrary JSON and tries to find a reasonable
// comment count by looking for keys like commentCount or arrays named comments.
func findCommentCountInJSONBytes(body []byte) int {
	var data interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0
	}
	return findCommentCountInJSON(data)
}

func findCommentCountInJSON(v interface{}) int {
	switch vv := v.(type) {
	case map[string]interface{}:
		max := 0
		for k, val := range vv {
			key := strings.ToLower(k)
			if key == "commentcount" || strings.HasSuffix(key, "commentcount") || key == "commentscount" || key == "comments_count" || key == "comment_count" || strings.Contains(key, "comment") {
				switch x := val.(type) {
				case float64:
					if int(x) > max {
						max = int(x)
					}
				case string:
					if n, err := strconv.Atoi(x); err == nil && n > max {
						max = n
					}
				case []interface{}:
					if len(x) > max {
						max = len(x)
					}
				}
			}
			if c := findCommentCountInJSON(val); c > max {
				max = c
			}
		}
		return max
	case []interface{}:
		max := 0
		for _, el := range vv {
			if c := findCommentCountInJSON(el); c > max {
				max = c
			}
		}
		return max
	default:
		return 0
	}
}

// parseTossDate는 다양한 문자열 날짜 포맷을 해석합니다
func parseTossDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// 구분점 등으로 이어진 경우 앞쪽 토큰만 취함
	if strings.Contains(s, "·") {
		parts := strings.Split(s, "·")
		if len(parts) > 0 {
			s = strings.TrimSpace(parts[0])
		}
	}
	if strings.Contains(s, " . ") {
		parts := strings.Split(s, " . ")
		if len(parts) > 0 {
			s = strings.TrimSpace(parts[0])
		}
	}
	// 1) RFC3339
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts, true
	}
	// 2) YYYY-MM-DD
	if ts, err := time.Parse("2006-01-02", s); err == nil {
		return ts, true
	}
	// 3) YYYY.MM.DD
	if ts, err := time.Parse("2006.01.02", s); err == nil {
		return ts, true
	}
	// 4) YYYY/MM/DD
	if ts, err := time.Parse("2006/01/02", s); err == nil {
		return ts, true
	}
	// 5) 한국어 포맷: 2025년 3월 18일
	krRe := regexp.MustCompile(`(?P<y>\d{4})\s*년\s*(?P<m>\d{1,2})\s*월\s*(?P<d>\d{1,2})\s*일`)
	if m := krRe.FindStringSubmatch(s); len(m) == 4 {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		loc, _ := time.LoadLocation("Asia/Seoul")
		return time.Date(y, time.Month(mo), d, 0, 0, 0, 0, loc), true
	}
	return time.Time{}, false
}

func sanitizeAuthor(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return author
	}
	// 날짜 패턴 제거
	krRe := regexp.MustCompile(`\d{4}\s*년\s*\d{1,2}\s*월\s*\d{1,2}\s*일`)
	author = krRe.ReplaceAllString(author, "")
	// 구분점 기준 오른쪽 토큰이 이름일 가능성이 크면 선택
	if strings.Contains(author, " · ") {
		parts := strings.Split(author, " · ")
		author = strings.TrimSpace(parts[len(parts)-1])
	}
	if strings.Contains(author, " . ") {
		parts := strings.Split(author, " . ")
		author = strings.TrimSpace(parts[len(parts)-1])
	}
	return strings.TrimSpace(author)
}
