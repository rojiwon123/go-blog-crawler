package models

import "time"

// BlogPost는 스크래핑된 블로그 포스트 정보를 담습니다
type BlogPost struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Author      string    `json:"author"`
	PublishedAt time.Time `json:"published_at"`
	Thumbnail   string    `json:"thumbnail"`
	URL         string    `json:"url"`
	Source      string    `json:"source"`

	// 메타 정보 (상세 페이지에서 추출)
	Favorites int      `json:"favorites"` // Medium의 관심수 (claps)
	Comments  int      `json:"comments"`  // 댓글 수
	Keywords  []string `json:"keywords"`  // 키워드/태그
}
