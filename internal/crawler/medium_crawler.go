package crawler

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"go-blog-crawler/internal/models"

	"github.com/chromedp/chromedp"
)

// MediumCrawler는 Medium 블로그 크롤링을 위한 인터페이스입니다
type MediumCrawler interface {
	// Crawl은 지정된 날짜 이내의 포스트를 크롤링하고 메타 정보까지 추출합니다
	Crawl(ctx context.Context, lastPublishedAt time.Time) ([]models.BlogPost, error)
	Close() error
}

// MediumCrawlerConfig는 Medium 크롤러 설정을 담습니다
type MediumCrawlerConfig struct {
	Path          string        // /daangn/all 같은 경로
	Source        string        // daangn 같은 소스명
	Title         string        // "당근 기술 블로그" 같은 제목
	MaxScrolls    int           // 최대 스크롤 횟수
	ScrollDelay   time.Duration // 스크롤 후 대기 시간
	InitialDelay  time.Duration // 초기 대기 시간 (Cloudflare 우회용)
	Headless      bool          // 헤드리스 모드 여부
	MaxConcurrent int           // 최대 동시 상세 페이지 접속 수
	Timeout       time.Duration // 크롤링 타임아웃
}

// DefaultMediumConfig는 기본 Medium 크롤러 설정을 반환합니다
func DefaultMediumConfig() MediumCrawlerConfig {
	return MediumCrawlerConfig{
		MaxScrolls:    100,
		ScrollDelay:   2 * time.Second,
		InitialDelay:  45 * time.Second,
		Headless:      true,
		MaxConcurrent: 10,
		Timeout:       5 * time.Minute,
	}
}

// baseMediumCrawler는 Medium 크롤러의 기본 구현을 담습니다
type baseMediumCrawler struct {
	ctx    context.Context
	cancel context.CancelFunc
	config MediumCrawlerConfig
}

// NewMediumCrawler는 새로운 Medium 크롤러를 생성합니다
func NewMediumCrawler(config MediumCrawlerConfig) (MediumCrawler, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

	crawler := &baseMediumCrawler{
		ctx:    ctx,
		cancel: cancel,
		config: config,
	}

	return crawler, nil
}

// getFullURL은 전체 URL을 반환합니다
func (c *baseMediumCrawler) getFullURL() string {
	baseURL := "https://medium.com"
	if c.config.Path != "" {
		return baseURL + c.config.Path
	}
	return baseURL
}

// Crawl은 지정된 날짜까지 블로그 포스트를 크롤링합니다
func (c *baseMediumCrawler) Crawl(ctx context.Context, lastPublishedAt time.Time) ([]models.BlogPost, error) {
	log.Printf("🚀 %s 크롤링 시작", c.config.Title)
	log.Printf("📅 %s 이전 글까지 수집", lastPublishedAt.Format("2006-01-02"))

	// Chrome 옵션 설정
	opts := c.getChromeOptions()

	// Chrome 실행 컨텍스트 생성
	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	// 크롤링 컨텍스트 생성
	taskCtx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancel()

	// 타임아웃 설정
	taskCtx, cancel = context.WithTimeout(taskCtx, c.config.Timeout)
	defer cancel()

	// Medium 페이지로 이동
	if err := chromedp.Run(taskCtx, chromedp.Navigate(c.getFullURL())); err != nil {
		return nil, fmt.Errorf("페이지 이동 실패: %v", err)
	}

	// Cloudflare 보안 검증 대기
	if err := c.waitForCloudflareChallenge(taskCtx); err != nil {
		return nil, fmt.Errorf("Cloudflare Challenge 실패: %v", err)
	}

	// 자동화 탐지 제거
	if err := c.removeAutomationDetection(taskCtx); err != nil {
		return nil, fmt.Errorf("자동화 탐지 제거 실패: %v", err)
	}

	// 스크롤 및 포스트 수집
	posts, err := c.scrollAndCollectPosts(taskCtx, lastPublishedAt)
	if err != nil {
		return nil, fmt.Errorf("포스트 수집 실패: %v", err)
	}

	// 중복 제거
	posts = c.removeDuplicates(posts)
	log.Printf("중복 제거 후: %d개 포스트", len(posts))

	// 메타 정보 추출
	if len(posts) > 0 {
		log.Printf("🔍 메타 정보 추출 시작...")
		posts, err = c.extractMetaInfoParallel(ctx, posts)
		if err != nil {
			log.Printf("⚠️  메타 정보 추출 실패: %v", err)
		} else {
			log.Printf("✅ 메타 정보 추출 완료")
		}
	}

	log.Printf("크롤링 완료: 총 %d개 포스트 수집", len(posts))
	return posts, nil
}

// Close는 크롤러를 종료합니다
func (c *baseMediumCrawler) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// getChromeOptions는 Chrome 옵션을 반환합니다
func (c *baseMediumCrawler) getChromeOptions() []chromedp.ExecAllocatorOption {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", c.config.Headless),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		// 실제 브라우저처럼 보이도록 설정
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-extensions", false),
		chromedp.Flag("disable-plugins", false),
		chromedp.Flag("disable-images", false),
		chromedp.Flag("disable-javascript", false),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-features", "TranslateUI"),
		chromedp.Flag("disable-ipc-flooding-protection", true),
		// 실제 브라우저 User-Agent 설정
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
		// 창 크기 설정
		chromedp.Flag("window-size", "1920,1080"),
		chromedp.Flag("start-maximized", true),
	)
	return opts
}

// extractPosts는 페이지에서 포스트 정보를 추출합니다
func (c *baseMediumCrawler) extractPosts(posts *[]models.BlogPost) chromedp.ActionFunc {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		// JavaScript로 포스트 정보 추출
		var result interface{}
		if err := chromedp.Run(ctx, chromedp.Evaluate(`
			(function() {
				const posts = [];
				const uniquePosts = new Map(); // 중복 방지를 위한 Map 사용
				
				// 모든 article 요소를 찾아서 포스트 정보 추출
				const articles = document.querySelectorAll('article[data-testid="post-preview"]');
				
				console.log('찾은 article 수:', articles.length);
				
				articles.forEach((article, index) => {
					// 포스트 링크 찾기
					const link = article.querySelector('a[href*="/daangn/"]');
					if (!link || !link.href) {
						return;
					}
					
					const urlPath = link.href.split('/');
					
					// 포스트 링크만 필터링 (URL 경로가 충분히 길고, /subpage/나 /all?가 아닌 경우)
					if (link.href.includes('/daangn/') && urlPath.length > 4 && 
						!link.href.includes('/subpage/') && !link.href.includes('/all?')) {
						
						// 이미 처리한 URL인지 확인
						if (uniquePosts.has(link.href)) {
							return;
						}
						
						// 제목 추출
						let title = '';
						const titleElement = article.querySelector('h2, h3, h4, [data-testid*="title"], .title, .post-title');
						if (titleElement) {
							title = titleElement.textContent.trim();
						}
						
						// 설명 추출
						let description = '';
						const descElement = article.querySelector('h3, p, [data-testid*="description"], .description, .post-description');
						if (descElement) {
							description = descElement.textContent.trim();
						}
						
						// 작성자 추출
						let author = '당근'; // 기본값
						const authorElements = article.querySelectorAll('p, span, div');
						for (const elem of authorElements) {
							const text = elem.textContent.trim();
							// 날짜 패턴이 아닌 텍스트를 작성자로 간주
							if (text && text.length > 0 && text.length < 50 && 
								!text.match(/^(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}/) &&
								!text.match(/^\d{4}-\d{2}-\d{2}/) &&
								!text.includes('Added') &&
								!text.includes('ago') &&
								!text.includes('min') &&
								!text.includes('hour') &&
								!text.includes('day') &&
								!text.includes('week') &&
								!text.includes('month') &&
								!text.includes('year')) {
								author = text;
								break;
							}
						}
						
						// 작성일 추출 - article 내부에서 "Added" 텍스트가 포함된 span 찾기
						let publishedAt = '';
						const addedSpans = article.querySelectorAll('span');
						for (const span of addedSpans) {
							const text = span.textContent.trim();
							if (text.match(/^(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}/)) {
								publishedAt = 'Added ' + text;
								break;
							}
						}
						
						// 썸네일 추출
						let thumbnail = '';
						const imgElement = article.querySelector('img');
						if (imgElement && imgElement.src) {
							thumbnail = imgElement.src;
						}
						
						// 유효한 포스트만 추가 (제목과 날짜가 모두 있어야 함)
						if (title && title.length > 0 && link.href && link.href.length > 0 && publishedAt) {
							const postData = {
								title: title,
								description: description || '',
								author: author,
								publishedAt: publishedAt,
								thumbnail: thumbnail || '',
								url: link.href
							};
							
							uniquePosts.set(link.href, postData);
							posts.push(postData);
							
							console.log('포스트 추가:', index + 1, title, publishedAt);
						}
					}
				});
				
				console.log('최종 추출된 포스트 수:', posts.length);
				return Array.from(uniquePosts.values());
			})();
		`, &result)); err != nil {
			return err
		}

		// JavaScript 결과를 BlogPost 구조체로 변환
		if postsArray, ok := result.([]interface{}); ok {
			// 기존 포스트 URL들을 Set으로 관리하여 중복 방지
			existingURLs := make(map[string]bool)
			for _, existingPost := range *posts {
				existingURLs[existingPost.URL] = true
			}

			for _, postData := range postsArray {
				if postMap, ok := postData.(map[string]interface{}); ok {
					// URL 추출
					var url string
					if urlVal, ok := postMap["url"].(string); ok {
						url = urlVal
					}

					// 이미 존재하는 포스트인지 확인
					if url == "" || existingURLs[url] {
						continue
					}

					post := models.BlogPost{
						Source: c.config.Source,
					}

					// 제목
					if title, ok := postMap["title"].(string); ok {
						post.Title = title
					}

					// 설명
					if description, ok := postMap["description"].(string); ok {
						post.Description = description
					}

					// 작성자
					if author, ok := postMap["author"].(string); ok {
						post.Author = author
					}

					// 작성일
					if publishedAt, ok := postMap["publishedAt"].(string); ok {
						if parsedDate, err := c.parseDate(publishedAt); err == nil {
							post.PublishedAt = parsedDate
						} else {
							// 날짜 파싱 실패 시 현재 시간으로 설정 (0001-01-01 방지)
							log.Printf("⚠️ 날짜 파싱 실패 (%s): %v, 현재 시간으로 설정", publishedAt, err)
							post.PublishedAt = time.Now()
						}
					} else {
						// publishedAt이 없는 경우 현재 시간으로 설정
						post.PublishedAt = time.Now()
					}

					// 썸네일
					if thumbnail, ok := postMap["thumbnail"].(string); ok {
						post.Thumbnail = thumbnail
					}

					// URL
					post.URL = url

					// 유효한 포스트만 추가
					if post.Title != "" && post.URL != "" {
						*posts = append(*posts, post)
						existingURLs[url] = true // 추가된 URL을 기록
					}
				}
			}
		}

		return nil
	})
}

// shouldStop은 크롤링을 중단해야 하는지 확인합니다
func (c *baseMediumCrawler) shouldStop(posts []models.BlogPost, lastPublishedAt time.Time) bool {
	if len(posts) == 0 {
		return false
	}

	// 가장 오래된 포스트 찾기
	oldestPost := posts[0]
	for _, post := range posts {
		if post.PublishedAt.Before(oldestPost.PublishedAt) {
			oldestPost = post
		}
	}

	// 지정된 날짜 이전 글이면 중단 (더 오래된 글을 찾을 필요 없음)
	if oldestPost.PublishedAt.Before(lastPublishedAt) {
		log.Printf("🛑 크롤링 중단 조건 충족: 가장 오래된 글 %s (%s 이전)",
			oldestPost.PublishedAt.Format("2006-01-02"), lastPublishedAt.Format("2006-01-02"))
		return true
	}

	return false
}

// saveHTMLToFile은 HTML 내용을 파일로 저장합니다
func (c *baseMediumCrawler) saveHTMLToFile(htmlContent, filename string) error {
	fullHTML := `<!DOCTYPE html>
<html lang="ko">
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>Medium Page Snapshot</title>
	<style>
		body { font-family: Arial, sans-serif; margin: 20px; }
		.info { background: #f0f0f0; padding: 15px; border-radius: 5px; margin-bottom: 20px; }
		.timestamp { color: #666; font-size: 12px; }
	</style>
</head>
<body>
	<div class="info">
		<h1>Medium Page Snapshot</h1>
		<p class="timestamp">저장 시간: ` + time.Now().Format("2006-01-02 15:04:05") + `</p>
		<p>이 파일은 Medium 페이지의 동적 렌더링 결과를 캡처한 것입니다.</p>
	</div>
	<hr>
	` + htmlContent + `
</body>
</html>`

	return os.WriteFile(filename, []byte(fullHTML), 0644)
}

// parseDate는 다양한 날짜 형식을 파싱합니다
func (c *baseMediumCrawler) parseDate(dateStr string) (time.Time, error) {
	if dateStr == "" {
		return time.Now(), fmt.Errorf("빈 날짜 문자열")
	}

	// Medium 형식: "Added Jul 31", "Added Jul 24" 등
	if strings.HasPrefix(dateStr, "Added ") {
		datePart := strings.TrimPrefix(dateStr, "Added ")

		// "Dec 26, 2024" 같은 형식 처리
		if strings.Contains(datePart, ",") {
			// "Dec 26, 2024" 형식
			if t, err := time.Parse("Jan 2, 2006", datePart); err == nil {
				return t, nil
			}
		}

		// 월과 일만 있는 형식 처리
		formats := []string{
			"Jan 2", "Feb 2", "Mar 2", "Apr 2", "May 2", "Jun 2",
			"Jul 2", "Aug 2", "Sep 2", "Oct 2", "Nov 2", "Dec 2",
		}

		for _, format := range formats {
			if t, err := time.Parse(format, datePart); err == nil {
				// 현재 시간과 비교하여 연도 추론
				now := time.Now()
				currentYear := now.Year()

				// 파싱된 날짜를 현재 연도로 설정
				parsedDate := time.Date(currentYear, t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)

				// 만약 파싱된 날짜가 미래라면 (예: 7월 31일이 현재 8월이라면), 작년으로 설정
				if parsedDate.After(now) {
					parsedDate = time.Date(currentYear-1, t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
				}

				return parsedDate, nil
			}
		}
	}

	// RFC3339 형식: "2024-12-31T00:00:00Z"
	if t, err := time.Parse(time.RFC3339, dateStr); err == nil {
		return t, nil
	}

	// "2006-01-02" 형식
	if t, err := time.Parse("2006-01-02", dateStr); err == nil {
		return t, nil
	}

	// ISO 형식: "2024-12-31"
	if t, err := time.Parse("2006-01-02", dateStr); err == nil {
		return t, nil
	}

	// "Jan 2, 2006" 형식 (연도 포함)
	if t, err := time.Parse("Jan 2, 2006", dateStr); err == nil {
		return t, nil
	}

	// 날짜 파싱 실패 시 현재 시간 반환 (에러와 함께)
	return time.Now(), fmt.Errorf("날짜 파싱 실패: %s", dateStr)
}

// containsPost는 포스트가 이미 존재하는지 확인합니다
func containsPost(posts []models.BlogPost, post models.BlogPost) bool {
	for _, existingPost := range posts {
		if existingPost.URL == post.URL {
			return true
		}
	}
	return false
}

// removeAutomationDetection은 자동화 탐지를 제거합니다
func (c *baseMediumCrawler) removeAutomationDetection(ctx context.Context) error {
	log.Printf("자동화 탐지 제거 중...")
	stealthScript := `
	() => {
		// webdriver 속성 제거
		Object.defineProperty(navigator, 'webdriver', {
			get: () => undefined,
		});
		
		// chrome 속성 제거
		delete window.chrome;
		
		// permissions 속성 제거
		delete navigator.permissions;
		
		// plugins 배열 수정
		Object.defineProperty(navigator, 'plugins', {
			get: () => [1, 2, 3, 4, 5],
		});
		
		// languages 속성 수정
		Object.defineProperty(navigator, 'languages', {
			get: () => ['ko-KR', 'ko', 'en-US', 'en'],
		});
		
		// userAgent 수정
		Object.defineProperty(navigator, 'userAgent', {
			get: () => 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
		});
		
		console.log('자동화 탐지 제거 완료');
		return 'stealth mode enabled';
	}
	`
	return chromedp.Run(ctx, chromedp.Evaluate(stealthScript, nil))
}

// waitForCloudflareChallenge는 Cloudflare Challenge 완료를 기다립니다
func (c *baseMediumCrawler) waitForCloudflareChallenge(ctx context.Context) error {
	var challengeText string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.textContent : ''`, &challengeText)); err != nil {
		return fmt.Errorf("Challenge 텍스트 확인 실패: %v", err)
	}

	if strings.Contains(challengeText, "Enable JavaScript and cookies to continue") {
		log.Printf("⚠️  Cloudflare Challenge가 아직 진행 중입니다. 더 기다립니다...")
		time.Sleep(30 * time.Second)
	} else {
		log.Printf("✅ Cloudflare Challenge 완료됨")
	}
	return nil
}

// scrollAndCollectPosts는 스크롤하면서 포스트를 수집합니다
func (c *baseMediumCrawler) scrollAndCollectPosts(ctx context.Context, lastPublishedAt time.Time) ([]models.BlogPost, error) {
	var posts []models.BlogPost

	scrollCount := 0
	lastHeight := int64(0)
	maxScrolls := c.config.MaxScrolls
	noChangeCount := 0      // 높이 변화가 없는 횟수
	maxNoChangeCount := 20  // 높이 변화가 없어도 더 오래 기다림
	lastPostCount := 0      // 마지막 포스트 수
	noNewPostCount := 0     // 새로운 포스트가 없는 횟수
	maxNoNewPostCount := 15 // 새로운 포스트가 없어도 더 오래 기다림

	for scrollCount < maxScrolls {
		// 현재 페이지 높이 확인
		var currentHeight int64
		if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body.scrollHeight`, &currentHeight)); err != nil {
			log.Printf("페이지 높이 확인 실패: %v", err)
			break
		}

		// 현재 포스트 수 확인
		oldPostCount := len(posts)
		if err := chromedp.Run(ctx, c.extractPosts(&posts)); err != nil {
			log.Printf("포스트 추출 실패: %v", err)
			break
		}
		newCount := len(posts) - oldPostCount

		// 현재 수집된 포스트 중 가장 최신 글 확인
		if len(posts) > 0 {
			newestPost := posts[0]
			for _, post := range posts {
				if post.PublishedAt.After(newestPost.PublishedAt) {
					newestPost = post
				}
			}

			// 지정된 날짜 이후 글이면 중단
			if c.shouldStop(posts, lastPublishedAt) {
				log.Printf("🛑 크롤링 중단: %s 이후 글 발견", lastPublishedAt.Format("2006-01-02"))
				break
			}

			log.Printf("스크롤 %d: 현재 포스트 수 %d (+%d), 가장 최신 글: %s (%d년 %d월)",
				scrollCount+1, len(posts), newCount, newestPost.Title, newestPost.PublishedAt.Year(), newestPost.PublishedAt.Month())
		}

		// 페이지 높이 변화 확인
		if currentHeight == lastHeight {
			noChangeCount++
			log.Printf("페이지 높이 변화 없음 (%d번째)", noChangeCount)
		} else {
			noChangeCount = 0
		}

		// 새로운 포스트가 없는지 확인
		if len(posts) == lastPostCount {
			noNewPostCount++
			log.Printf("새로운 포스트 없음 (%d번째)", noNewPostCount)
		} else {
			noNewPostCount = 0
		}

		// 더 이상 스크롤할 수 없거나 지정된 날짜 이후 포스트가 나오면 중단
		if (noChangeCount >= maxNoChangeCount && noNewPostCount >= maxNoNewPostCount) || c.shouldStop(posts, lastPublishedAt) {
			if noChangeCount >= maxNoChangeCount && noNewPostCount >= maxNoNewPostCount {
				log.Printf("페이지 높이 변화가 %d번 연속 없고, 새로운 포스트도 %d번 연속 없어서 크롤링 중단", maxNoChangeCount, maxNoNewPostCount)
			} else {
				log.Printf("지정된 날짜 이후 포스트 발견으로 크롤링 중단")
			}
			break
		}

		// 스크롤 다운
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil)); err != nil {
			log.Printf("스크롤 실패: %v", err)
			break
		}

		// 스크롤 후 페이지 로딩 대기
		scrollWaitTime := c.config.ScrollDelay * 5
		log.Printf("스크롤 후 %d초 대기 중...", scrollWaitTime/time.Second)
		time.Sleep(scrollWaitTime)

		// 페이지가 완전히 로드될 때까지 대기
		if err := chromedp.Run(ctx, chromedp.WaitReady("body")); err != nil {
			log.Printf("페이지 로딩 대기 실패: %v", err)
		}

		lastHeight = currentHeight
		lastPostCount = len(posts)
		scrollCount++
	}

	return posts, nil
}

// removeDuplicates는 중복된 포스트를 제거합니다
func (c *baseMediumCrawler) removeDuplicates(posts []models.BlogPost) []models.BlogPost {
	seen := make(map[string]bool)
	var result []models.BlogPost

	for _, post := range posts {
		if !seen[post.URL] {
			seen[post.URL] = true
			result = append(result, post)
		}
	}

	return result
}

// extractPostMetaInfo는 개별 포스트의 메타 정보를 추출합니다
func (c *baseMediumCrawler) extractPostMetaInfo(ctx context.Context, postURL string) (*models.BlogPost, error) {
	// 새로운 Chrome 컨텍스트 생성 (기존과 분리)
	opts := c.getChromeOptions()
	allocCtx, cancel := chromedp.NewExecAllocator(ctx, opts...)
	defer cancel()

	taskCtx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	defer cancel()

	// 포스트 페이지로 이동
	if err := chromedp.Run(taskCtx, chromedp.Navigate(postURL)); err != nil {
		return nil, fmt.Errorf("포스트 페이지 이동 실패: %v", err)
	}

	// 페이지 로딩 대기 (더 오래 기다림)
	if err := chromedp.Run(taskCtx, chromedp.WaitReady("body")); err != nil {
		return nil, fmt.Errorf("페이지 로딩 대기 실패: %v", err)
	}

	// 추가 대기 시간 (동적 콘텐츠 로딩을 위해)
	time.Sleep(5 * time.Second)

	// 페이지를 스크롤하여 모든 콘텐츠가 로드되도록 함
	if err := chromedp.Run(taskCtx, chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil)); err != nil {
		log.Printf("페이지 스크롤 실패: %v", err)
	}

	// 스크롤 후 추가 대기
	time.Sleep(2 * time.Second)

	// 메타 정보 추출
	var metaInfo models.BlogPost
	if err := chromedp.Run(taskCtx, c.extractPostMetaInfoJS(&metaInfo)); err != nil {
		return nil, fmt.Errorf("메타 정보 추출 실패: %v", err)
	}

	return &metaInfo, nil
}

// extractMetaInfoParallel는 포스트들의 메타 정보를 병렬로 추출합니다
func (c *baseMediumCrawler) extractMetaInfoParallel(ctx context.Context, posts []models.BlogPost) ([]models.BlogPost, error) {
	log.Printf("🔍 메타 정보 추출 시작: %d개 포스트", len(posts))

	// 세마포어를 사용하여 동시 접속 수 제한
	semaphore := make(chan struct{}, c.config.MaxConcurrent)

	// 결과를 저장할 슬라이스 (원본과 동일한 순서 유지)
	result := make([]models.BlogPost, len(posts))
	copy(result, posts)

	// 동시성 제어를 위한 WaitGroup
	var wg sync.WaitGroup

	// 각 포스트에 대해 메타 정보 추출 (순서 보장)
	for i, post := range posts {
		wg.Add(1)
		go func(index int, postURL string, postTitle string) {
			defer wg.Done()

			// 세마포어 획득
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			log.Printf("🔍 포스트 %d 메타 정보 추출 시작: %s", index+1, postTitle)

			// 메타 정보 추출
			if metaInfo, err := c.extractPostMetaInfo(ctx, postURL); err != nil {
				log.Printf("⚠️  포스트 %d 메타 정보 추출 실패: %v", index+1, err)
			} else {
				// 메타 정보 업데이트
				result[index].Favorites = metaInfo.Favorites
				result[index].Comments = metaInfo.Comments
				result[index].Keywords = metaInfo.Keywords

				log.Printf("✅ 포스트 %d 메타 정보 추출 완료: %s - Favorites=%d, Comments=%d, Keywords=%v",
					index+1, postTitle, metaInfo.Favorites, metaInfo.Comments, metaInfo.Keywords)
			}
		}(i, post.URL, post.Title)
	}

	// 모든 고루틴 완료 대기
	wg.Wait()

	// 결과 검증 및 로깅
	log.Printf("=== 메타 정보 추출 결과 검증 ===")
	for i, post := range result {
		log.Printf("포스트 %d: %s - Favorites=%d, Comments=%d",
			i+1, post.Title, post.Favorites, post.Comments)
	}

	log.Printf("🎯 메타 정보 추출 완료: %d개 포스트", len(result))
	return result, nil
}

// extractPostMetaInfoJS는 개별 포스트 페이지에서 메타 정보를 추출하는 JavaScript입니다
func (c *baseMediumCrawler) extractPostMetaInfoJS(result *models.BlogPost) chromedp.ActionFunc {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		script := `
		(function() {
			console.log('=== 메타 정보 추출 시작 ===');
			console.log('현재 페이지 URL:', window.location.href);
			console.log('페이지 제목:', document.title);
			
			// 결과 객체 초기화
			const result = {
				favorites: 0,
				comments: 0,
				keywords: []
			};
			
			// footer element 찾기
			console.log('=== Footer element 찾기 ===');
			const footer = document.querySelector('footer');
			if (footer) {
				console.log('Footer element 발견:', footer);
				console.log('Footer HTML:', footer.innerHTML);
				
				// footer 내부의 모든 button과 span 요소들 확인
				const footerButtons = footer.querySelectorAll('button');
				const footerSpans = footer.querySelectorAll('span');
				console.log('Footer 내 button 수:', footerButtons.length);
				console.log('Footer 내 span 수:', footerSpans.length);
				
				// 각 button과 span의 정보 출력
				footerButtons.forEach((btn, index) => {
					console.log('Footer Button', index, ':', {
						text: btn.textContent,
						className: btn.className,
						ariaLabel: btn.getAttribute('aria-label'),
						value: btn.getAttribute('value'),
						innerHTML: btn.innerHTML
					});
				});
				
				footerSpans.forEach((span, index) => {
					console.log('Footer Span', index, ':', {
						text: span.textContent,
						className: span.className,
						innerHTML: span.innerHTML
					});
				});
				
				// 관심수 (claps) 찾기 - footer 내에서 clap 관련 요소 찾기
				try {
					console.log('=== Footer에서 관심수 추출 시도 ===');
					
					// clap 관련 button 찾기 (더 구체적인 선택자 사용)
					const clapButtons = footer.querySelectorAll('button[aria-label*="clap"], button[aria-label*="like"], button[aria-label*="favorite"], button[data-testid*="clap"], button[data-testid*="like"]');
					console.log('Footer clap 관련 button 수:', clapButtons.length);
					
					for (let i = 0; i < clapButtons.length; i++) {
						const button = clapButtons[i];
						console.log('Clap button', i, ':', button.textContent, button.className, button.getAttribute('aria-label'));
						
						// button 내부의 숫자 찾기
						const buttonText = button.textContent || '';
						const numberMatch = buttonText.match(/\d+/);
						if (numberMatch) {
							const potentialFavorites = parseInt(numberMatch[0]);
							if (potentialFavorites >= 0 && potentialFavorites <= 10000) {
								result.favorites = potentialFavorites;
								console.log('Footer에서 관심수 찾음:', result.favorites);
								break;
							}
						}
					}
					
					// 만약 button에서 못찾았다면, footer 내의 숫자 텍스트가 있는 span 찾기
					if (result.favorites === 0) {
						const numberSpans = footer.querySelectorAll('span');
						for (let i = 0; i < numberSpans.length; i++) {
							const span = numberSpans[i];
							const spanText = span.textContent || '';
							const numberMatch = spanText.match(/^\d+$/);
							if (numberMatch && spanText.length <= 5) {
								const potentialFavorites = parseInt(numberMatch[0]);
								if (potentialFavorites >= 0 && potentialFavorites <= 10000) {
									result.favorites = potentialFavorites;
									console.log('Footer span에서 관심수 찾음:', result.favorites);
									break;
								}
							}
						}
					}
					
				} catch (e) {
					console.error('Footer 관심수 추출 오류:', e);
				}
				
				// 댓글 수 찾기 - footer 내에서 response/comment 관련 요소 찾기
				try {
					console.log('=== Footer에서 댓글수 추출 시도 ===');
					
					// response/comment 관련 button 찾기 (더 구체적인 선택자 사용)
					const responseButtons = footer.querySelectorAll('button[aria-label*="response"], button[aria-label*="comment"], button[aria-label*="discuss"], button[data-testid*="response"], button[data-testid*="comment"]');
					console.log('Footer response 관련 button 수:', responseButtons.length);
					
					for (let i = 0; i < responseButtons.length; i++) {
						const button = responseButtons[i];
						console.log('Response button', i, ':', button.textContent, button.className, button.getAttribute('aria-label'));
						
						// button 내부의 숫자 찾기
						const buttonText = button.textContent || '';
						const numberMatch = buttonText.match(/\d+/);
						if (numberMatch) {
							const potentialComments = parseInt(numberMatch[0]);
							if (potentialComments >= 0 && potentialComments <= 1000) {
								result.comments = potentialComments;
								console.log('Footer에서 댓글수 찾음:', result.comments);
								break;
							}
						}
					}
					
					// 만약 button에서 못찾았다면, footer 내의 숫자 텍스트가 있는 span 찾기
					if (result.comments === 0) {
						const numberSpans = footer.querySelectorAll('span');
						for (let i = 0; i < numberSpans.length; i++) {
							const span = numberSpans[i];
							const spanText = span.textContent || '';
							const numberMatch = spanText.match(/^\d+$/);
							if (numberMatch && spanText.length <= 4) {
								const potentialComments = parseInt(numberMatch[0]);
								if (potentialComments >= 0 && potentialComments <= 1000) {
									result.comments = potentialComments;
									console.log('Footer span에서 댓글수 찾음:', result.comments);
									break;
								}
							}
						}
					}
					
				} catch (e) {
					console.error('Footer 댓글수 추출 오류:', e);
				}
				
				// 중복 추출 방지: 관심수와 댓글수가 같은 경우 댓글수를 0으로 설정
				if (result.favorites > 0 && result.comments > 0 && result.favorites === result.comments) {
					console.log('관심수와 댓글수가 동일하여 중복 추출로 판단, 댓글수를 0으로 설정');
					result.comments = 0;
				}
				
			} else {
				console.log('Footer element를 찾을 수 없음');
			}
			
			// 키워드 추출
			try {
				console.log('=== Keywords 추출 시도 ===');
				const keywordElements = document.querySelectorAll('a[href*="/tag/"], a[href*="/topic/"]');
				
				for (let i = 0; i < keywordElements.length; i++) {
					const element = keywordElements[i];
					const keywordText = element.textContent ? element.textContent.trim() : '';
					if (keywordText && keywordText.length > 0) {
						result.keywords.push(keywordText);
					}
				}
				
				console.log('키워드:', result.keywords);
			} catch (e) {
				console.error('키워드 추출 오류:', e);
			}
			
			console.log('=== 최종 결과 ===');
			console.log('Favorites:', result.favorites);
			console.log('Comments:', result.comments);
			console.log('Keywords:', result.keywords);
			
			return result;
		})();
		`

		return chromedp.Evaluate(script, result).Do(ctx)
	})
}
