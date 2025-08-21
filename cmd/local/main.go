package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"go-blog-scraper/internal/models"
	"go-blog-scraper/internal/scraper"
)

func main() {
	// 토스 스크래퍼 설정 (Medium 비활성화)
	tossCfg := scraper.DefaultTossConfig()
	// 기본: 개발 카테고리만 테스트. 필요 시 첫 번째 인자를 CSV로 받아 덮어씀 (예: "tech,data-ml")
	tossCfg.CategoryPaths = []string{"tech"}
	tossCfg.MaxPages = 3
	tossCfg.Headless = true
	tossCfg.Timeout = 2 * time.Minute
	tossCfg.InitialDelay = 0
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		cats := strings.Split(os.Args[1], ",")
		var trimmed []string
		for _, c := range cats {
			c = strings.TrimSpace(c)
			if c != "" {
				trimmed = append(trimmed, c)
			}
		}
		if len(trimmed) > 0 {
			tossCfg.CategoryPaths = trimmed
		}
	}

	log.Printf("🚀 토스 스크래핑 시작")
	log.Printf("🗂️  카테고리: %s", strings.Join(tossCfg.CategoryPaths, ", "))
	log.Printf("🏷️  소스: %s", tossCfg.Title)

	scraperInstance, err := scraper.NewTossScraper(tossCfg)
	if err != nil {
		log.Fatalf("❌ 스크래핑 생성 실패: %v", err)
	}
	defer scraperInstance.Close()

	// 검증 모드: 테스트 JSON과 라이브 스크랩 결과를 비교
	if os.Getenv("TOSS_VALIDATE") == "1" {
		testPath := strings.TrimSpace(os.Getenv("TOSS_TEST_JSON"))
		if testPath == "" {
			log.Fatalf("❌ 검증 모드: TOSS_TEST_JSON이 필요합니다")
		}
		expected, err := loadTossTestPosts(testPath, tossCfg.Source)
		if err != nil {
			log.Fatalf("❌ 테스트 데이터 로드 실패: %v", err)
		}
		// 검증은 tech 1페이지만 대상으로 수행
		tossCfg.CategoryPaths = []string{"tech"}
		tossCfg.MaxPages = 1
		// 재생성
		scraperInstance, err = scraper.NewTossScraper(tossCfg)
		if err != nil {
			log.Fatalf("❌ 스크래퍼 재생성 실패: %v", err)
		}
		defer scraperInstance.Close()

		lastPublishedAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		actual, err := scraperInstance.Scrape(context.Background(), lastPublishedAt)
		if err != nil {
			log.Fatalf("❌ 라이브 스크래핑 실패: %v", err)
		}
		// 상위 N 비교 (N = expected 길이)
		n := len(expected)
		if len(actual) < n {
			log.Fatalf("❌ 라이브 결과 부족: 기대 %d개, 실제 %d개", n, len(actual))
		}
		mismatches := compareTopN(expected, actual[:n])
		if len(mismatches) > 0 {
			for _, msg := range mismatches {
				log.Printf("❌ 불일치: %s", msg)
			}
			log.Fatalf("❌ 검증 실패: %d개 항목 불일치", len(mismatches))
		}
		log.Printf("✅ 검증 성공: 상위 %d개가 테스트 케이스와 일치", n)
		printResults(actual[:n])
		printYearlyStats(actual[:n])
		printMetaInfoSummary(actual[:n])
		return
	}

	// 테스트 JSON이 지정된 경우, 파일에서 로드하여 출력 후 종료
	if testPath := os.Getenv("TOSS_TEST_JSON"); strings.TrimSpace(testPath) != "" {
		posts, err := loadTossTestPosts(testPath, tossCfg.Source)
		if err != nil {
			log.Fatalf("❌ 테스트 데이터 로드 실패: %v", err)
		}
		printResults(posts)
		printYearlyStats(posts)
		printMetaInfoSummary(posts)
		log.Printf("🎯 테스트 데이터 출력 완료: 총 %d개", len(posts))
		return
	}

	// 스크래핑할 마지막 날짜 설정 (2025년 1월 1일 이후)
	lastPublishedAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	log.Printf("📅 %s 이후 글까지 수집 예정", lastPublishedAt.Format("2006-01-02"))

	// 스크래핑 실행
	posts, err := scraperInstance.Scrape(context.Background(), lastPublishedAt)
	if err != nil {
		log.Fatalf("❌ 스크래핑 실패: %v", err)
	}

	// 결과 출력
	printResults(posts)
	printYearlyStats(posts)
	printMetaInfoSummary(posts)

	log.Printf("🎯 전체 작업 완료: 총 %d개 포스트 수집 및 메타 정보 추출", len(posts))
}

// loadTossTestPosts는 toss_test.json 형태의 데이터를 로드합니다
func loadTossTestPosts(path string, source string) ([]models.BlogPost, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	type testPost struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		PublishedAt string   `json:"publishedAt"`
		Author      string   `json:"author"`
		Thumbnail   string   `json:"thumbnail"`
		ToURL       string   `json:"toUrl"`
		Comments    int      `json:"comments"`
		Keywords    []string `json:"keywords"`
	}
	var arr []testPost
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, err
	}
	var out []models.BlogPost
	for _, it := range arr {
		var published time.Time
		if t, err := time.Parse("2006-01-02", strings.TrimSpace(it.PublishedAt)); err == nil {
			published = t
		}
		out = append(out, models.BlogPost{
			Title:       it.Title,
			Description: it.Description,
			Author:      it.Author,
			PublishedAt: published,
			Thumbnail:   it.Thumbnail,
			URL:         it.ToURL,
			Source:      source,
			Comments:    it.Comments,
			Keywords:    it.Keywords,
		})
	}
	return out, nil
}

// compareTopN은 기대/실제 상위 N 항목을 필드별로 비교합니다
func compareTopN(expected []models.BlogPost, actual []models.BlogPost) []string {
	var diffs []string
	minLen := len(expected)
	if len(actual) < minLen {
		minLen = len(actual)
	}
	for i := 0; i < minLen; i++ {
		e := expected[i]
		a := actual[i]
		if strings.TrimSpace(e.Title) != strings.TrimSpace(a.Title) {
			diffs = append(diffs, fmt.Sprintf("[%d] title: exp=%q act=%q", i+1, e.Title, a.Title))
		}
		if strings.TrimSpace(e.Description) != strings.TrimSpace(a.Description) {
			diffs = append(diffs, fmt.Sprintf("[%d] description: exp=%q act=%q", i+1, e.Description, a.Description))
		}
		if strings.TrimSpace(e.Author) != strings.TrimSpace(a.Author) {
			diffs = append(diffs, fmt.Sprintf("[%d] author: exp=%q act=%q", i+1, e.Author, a.Author))
		}
		if e.PublishedAt.Format("2006-01-02") != a.PublishedAt.Format("2006-01-02") {
			diffs = append(diffs, fmt.Sprintf("[%d] publishedAt: exp=%s act=%s", i+1, e.PublishedAt.Format("2006-01-02"), a.PublishedAt.Format("2006-01-02")))
		}
		if strings.TrimSpace(e.Thumbnail) != strings.TrimSpace(a.Thumbnail) {
			diffs = append(diffs, fmt.Sprintf("[%d] thumbnail: exp=%q act=%q", i+1, e.Thumbnail, a.Thumbnail))
		}
		if strings.TrimSpace(e.URL) != strings.TrimSpace(a.URL) {
			diffs = append(diffs, fmt.Sprintf("[%d] url: exp=%q act=%q", i+1, e.URL, a.URL))
		}
		if e.Comments != a.Comments {
			diffs = append(diffs, fmt.Sprintf("[%d] comments: exp=%d act=%d", i+1, e.Comments, a.Comments))
		}
	}
	return diffs
}

// printResults는 스크래핑 결과를 출력합니다
func printResults(posts []models.BlogPost) {
	fmt.Printf("\n📊 스크래핑 결과 요약\n")
	fmt.Printf("총 포스트 수: %d개\n", len(posts))
	if len(posts) > 0 {
		fmt.Printf("스크래핑 완료 시간: %s\n\n", posts[0].PublishedAt.Format("2006-01-02 15:04:05"))
	}

	// 각 포스트 정보 출력
	for i, post := range posts {
		fmt.Printf("📝 포스트 #%d\n", i+1)
		fmt.Printf("제목: %s\n", post.Title)
		fmt.Printf("설명: %s\n", truncateString(post.Description, 100))
		fmt.Printf("작성자: %s\n", post.Author)
		fmt.Printf("작성일: %s\n", post.PublishedAt.Format("2006-01-02"))
		fmt.Printf("썸네일: %s\n", post.Thumbnail)
		fmt.Printf("URL: %s\n", post.URL)
		fmt.Printf("출처: %s\n", post.Source)

		// 메타 정보 출력
		if post.Favorites > 0 || post.Comments > 0 || len(post.Keywords) > 0 {
			fmt.Printf("메타 정보:\n")
			if post.Favorites > 0 {
				fmt.Printf("  ❤️  Favorites: %d\n", post.Favorites)
			}
			if post.Comments > 0 {
				fmt.Printf("  💬 댓글: %d개\n", post.Comments)
			}
			if len(post.Keywords) > 0 {
				fmt.Printf("  🏷️  키워드: %s\n", strings.Join(post.Keywords, ", "))
			}
		}

		fmt.Printf("---\n\n")
	}
}

// printYearlyStats는 연도별 포스트 통계를 출력합니다
func printYearlyStats(posts []models.BlogPost) {
	yearCount := make(map[int]int)

	for _, post := range posts {
		year := post.PublishedAt.Year()
		yearCount[year]++
	}

	fmt.Printf("📈 연도별 포스트 통계\n")
	for year := 2025; year >= 2020; year-- {
		if count, exists := yearCount[year]; exists {
			fmt.Printf("%d년 포스트: %d개\n", year, count)
		}
	}
	fmt.Printf("전체 포스트: %d개\n", len(posts))
}

// printMetaInfoSummary는 메타 정보 요약을 출력합니다
func printMetaInfoSummary(posts []models.BlogPost) {
	totalFavorites := 0
	totalComments := 0
	categoryCount := make(map[string]int)
	postsWithMeta := 0

	for _, post := range posts {
		if post.Favorites > 0 || post.Comments > 0 || len(post.Keywords) > 0 {
			postsWithMeta++
		}
		totalFavorites += post.Favorites
		totalComments += post.Comments

		for _, category := range post.Keywords {
			categoryCount[category]++
		}
	}

	fmt.Printf("\n📊 메타 정보 요약\n")
	fmt.Printf("메타 정보가 있는 포스트: %d개\n", postsWithMeta)
	fmt.Printf("총 Favorites: %d\n", totalFavorites)
	fmt.Printf("총 댓글 수: %d\n", totalComments)

	if len(categoryCount) > 0 {
		fmt.Printf("\n🏷️  키워드별 포스트 수:\n")
		for category, count := range categoryCount {
			fmt.Printf("  %s: %d개\n", category, count)
		}
	}
}

// truncateString은 문자열을 지정된 길이로 자릅니다
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
