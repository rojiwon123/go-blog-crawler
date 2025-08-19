package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"go-blog-scraper/internal/models"
	"go-blog-scraper/internal/scraper"
)

func main() {
	// 기본 설정
	config := scraper.DefaultMediumConfig()

	// 당근 스크래핑 설정
	path := "/daangn/all"
	source := "daangn"
	title := "당근 기술 블로그"
	maxPosts := 5 // 테스트용으로 포스트 수 제한

	// 명령행 인자가 있으면 사용
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	if len(os.Args) > 2 {
		source = os.Args[2]
	}
	if len(os.Args) > 3 {
		title = os.Args[3]
	}
	if len(os.Args) > 4 {
		if max, err := fmt.Sscanf(os.Args[4], "%d", &maxPosts); err != nil || max != 1 {
			log.Printf("⚠️  포스트 수 제한 파싱 실패, 기본값 %d 사용", maxPosts)
		}
	}

	log.Printf("🚀 스크래핑 시작")
	log.Printf("📝 Path: %s", path)
	log.Printf("🏷️  소스: %s", title)

	// Path 유효성 검사
	if !strings.HasPrefix(path, "/") {
		log.Fatalf("❌ Path는 /로 시작해야 합니다: %s", path)
	}

	// 스크래핑 생성
	config.Path = path
	config.Source = source
	config.Title = title

	scraperInstance, err := scraper.NewMediumScraper(config)
	if err != nil {
		log.Fatalf("❌ 스크래핑 생성 실패: %v", err)
	}
	defer scraperInstance.Close()

	// 스크래핑할 마지막 날짜 설정 (2025년 7월 1일 이후)
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
