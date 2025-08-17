# Go Blog Crawler

Go 언어로 작성된 기술 블로그 크롤러입니다. chromedp를 사용하여 기술 블로그를 크롤링하고, 최신 태그 블로그 피드를 생성합니다.

## 🚀 주요 기능

- **웹 크롤링**: chromedp를 사용한 안정적인 웹 스크래핑
- **블로그 피드 생성**: 크롤링 결과를 HTML 형태로 변환
- **자동화**: 주기적 실행을 위한 스크립트 형태로 동작
- **최신 콘텐츠**: 최신 기술 블로그 포스트 수집

## 🛠️ 기술 스택

- **언어**: Go 1.24.5
- **웹 크롤링**: chromedp
- **빌드 도구**: Task
- **코드 품질**: golangci-lint, goimports-reviser

## 📦 설치 및 실행

### 사전 요구사항
- Go 1.24.5+
- Chrome/Chromium 브라우저

### 설치
```bash
# 의존성 설치
go mod tidy

# 개발 도구 설치
task setup:tools
```

### 실행
```bash
# 로컬에서 실행
task run:local

# 빌드
task build

# 코드 포맷팅
task format

# 린팅
task lint
```

## 📁 프로젝트 구조

```
├── cmd/           # 애플리케이션 진입점
├── internal/      # 내부 패키지
├── pkg/          # 공개 패키지
├── tools.go      # 개발 도구 의존성
└── TaskFile.yml  # 빌드 태스크 정의
```

## 🔧 개발

### 코드 품질 관리
- **포맷팅**: `task format`
- **린팅**: `task lint`
- **스테이지된 파일만**: `task format:staged`, `task lint:staged`

### 빌드
- **단일 플랫폼**: `task build`
- **멀티 플랫폼**: `task build:all`
- **빌드 정리**: `task clean:build`

## 📝 라이선스

이 프로젝트는 [LICENSE](LICENSE) 파일에 명시된 라이선스 하에 배포됩니다.
