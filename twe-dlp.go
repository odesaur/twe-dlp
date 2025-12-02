package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	twitchemotesBaseURL = "https://twitchemotes.com"
)

var (
	emoteSizeList      = []string{"1.0", "2.0", "3.0"}
	channelURLPattern  = regexp.MustCompile(`/channels/(\d+)`)
	htmlTagPattern     = regexp.MustCompile(`<.*?>`)
	safeNamePattern    = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	defaultUserAgent   = "Mozilla/5.0 (X11; Linux x86_64) twe-dlp/1.0"
	httpRequestTimeout = 30 * time.Second
)

type EmoteData struct {
	BaseURL    string
	FormatType string
	EmoteCode  string
}

func createHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpRequestTimeout,
	}
}

func makeSafeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "unknown"
	}
	safe := safeNamePattern.ReplaceAllString(name, "_")
	if safe == "" {
		return "unknown"
	}
	return safe
}

func readStdinLine(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func resolveChannelIdentifierToID(httpClient *http.Client, channelIdentifier string) (string, error) {
	if channelIdentifier == "" {
		return "", errors.New("empty channel identifier")
	}

	isNumeric := true
	for _, character := range channelIdentifier {
		if character < '0' || character > '9' {
			isNumeric = false
			break
		}
	}
	if isNumeric {
		return channelIdentifier, nil
	}

	formValues := url.Values{}
	formValues.Set("query", channelIdentifier)
	formValues.Set("source", "twe-dlp")

	requestURL := twitchemotesBaseURL + "/search/channel"
	request, err := http.NewRequest("POST", requestURL, strings.NewReader(formValues.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", defaultUserAgent)

	response, err := httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	finalURL := response.Request.URL.String()
	match := channelURLPattern.FindStringSubmatch(finalURL)
	if len(match) == 2 {
		return match[1], nil
	}

	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	bodyText := string(bodyBytes)
	match = channelURLPattern.FindStringSubmatch(bodyText)
	if len(match) == 2 {
		return match[1], nil
	}

	return "", fmt.Errorf("could not resolve channel name %q to an ID", channelIdentifier)
}

func fetchDocument(httpClient *http.Client, pageURL string) (*goquery.Document, *http.Response, error) {
	request, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("User-Agent", defaultUserAgent)

	response, err := httpClient.Do(request)
	if err != nil {
		return nil, nil, err
	}

	document, err := goquery.NewDocumentFromReader(response.Body)
	if err != nil {
		response.Body.Close()
		return nil, nil, err
	}

	return document, response, nil
}

func getChannelDisplayName(document *goquery.Document) string {
	headerSelection := document.Find("div.card-header").First()
	if headerSelection.Length() == 0 {
		return ""
	}

	anchorSelection := headerSelection.Find("a").First()
	if anchorSelection.Length() > 0 {
		text := strings.TrimSpace(anchorSelection.Text())
		if text != "" {
			return text
		}
	}

	headerTagSelection := headerSelection.Find("h1, h2, h3").First()
	if headerTagSelection.Length() > 0 {
		text := strings.TrimSpace(headerTagSelection.Text())
		if text != "" {
			return text
		}
	}

	return ""
}

func resolveRelativeURL(base string, relative string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	relativeURL, err := url.Parse(relative)
	if err != nil {
		return "", err
	}
	resolved := baseURL.ResolveReference(relativeURL)
	return resolved.String(), nil
}

func collectEmoteMetadata(document *goquery.Document) map[string]EmoteData {
	emoteMap := make(map[string]EmoteData)

	document.Find("img").Each(func(_ int, selection *goquery.Selection) {
		imageSource, hasSrc := selection.Attr("src")
		if !hasSrc || imageSource == "" {
			return
		}

		if !strings.Contains(imageSource, "static-cdn.jtvnw.net/emoticons/v2/") {
			return
		}

		fullImageSource := imageSource
		if !strings.HasPrefix(fullImageSource, "http://") && !strings.HasPrefix(fullImageSource, "https://") {
			resolved, err := resolveRelativeURL(twitchemotesBaseURL, fullImageSource)
			if err != nil {
				return
			}
			fullImageSource = resolved
		}

		pathParts := strings.Split(fullImageSource, "/")
		emoticonsIndex := -1
		for index, part := range pathParts {
			if part == "emoticons" {
				emoticonsIndex = index
				break
			}
		}
		if emoticonsIndex == -1 {
			return
		}
		if emoticonsIndex+3 >= len(pathParts) {
			return
		}

		emoteIdentifier := pathParts[emoticonsIndex+2]
		formatType := pathParts[emoticonsIndex+3]
		baseURL := strings.Join(pathParts[:emoticonsIndex+4], "/")

		emoteCode, hasRegex := selection.Attr("data-regex")
		if !hasRegex || strings.TrimSpace(emoteCode) == "" {
			tooltipHTML, hasTooltip := selection.Attr("data-tooltip")
			if hasTooltip && strings.TrimSpace(tooltipHTML) != "" {
				emoteCode = htmlTagPattern.ReplaceAllString(tooltipHTML, "")
				emoteCode = strings.TrimSpace(emoteCode)
			}
		}
		if emoteCode == "" {
			parentText := strings.TrimSpace(selection.Parent().Text())
			if parentText != "" {
				emoteCode = parentText
			} else {
				emoteCode = emoteIdentifier
			}
		}

		if _, exists := emoteMap[emoteIdentifier]; exists {
			return
		}

		emoteMap[emoteIdentifier] = EmoteData{
			BaseURL:    baseURL,
			FormatType: formatType,
			EmoteCode:  emoteCode,
		}
	})

	return emoteMap
}

func determineFileExtension(contentType string) string {
	contentType = strings.ToLower(contentType)
	if strings.Contains(contentType, "gif") {
		return "gif"
	}
	if strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg") {
		return "jpg"
	}
	if strings.Contains(contentType, "png") {
		return "png"
	}
	return "img"
}

func downloadEmoteImages(httpClient *http.Client, emoteIdentifier string, emoteData EmoteData, outputRoot string) {
	emoteCode := emoteData.EmoteCode
	emoteBaseURL := emoteData.BaseURL

	safeEmoteCode := makeSafeName(emoteCode)
	emoteFolder := filepath.Join(outputRoot, safeEmoteCode)
	err := os.MkdirAll(emoteFolder, 0o755)
	if err != nil {
		fmt.Printf("[error] cannot create folder %s: %v\n", emoteFolder, err)
		return
	}

	for _, sizeValue := range emoteSizeList {
		imageURL := fmt.Sprintf("%s/light/%s", emoteBaseURL, sizeValue)

		request, err := http.NewRequest("GET", imageURL, nil)
		if err != nil {
			fmt.Printf("[skip] %s (%v)\n", imageURL, err)
			continue
		}
		request.Header.Set("User-Agent", defaultUserAgent)

		response, err := httpClient.Do(request)
		if err != nil {
			fmt.Printf("[skip] %s (%v)\n", imageURL, err)
			continue
		}

		if response.StatusCode != http.StatusOK {
			fmt.Printf("[skip] %s (status %s)\n", imageURL, response.Status)
			response.Body.Close()
			continue
		}

		contentType := response.Header.Get("Content-Type")
		fileExtension := determineFileExtension(contentType)
		outputFilename := fmt.Sprintf("%s_%s.%s", safeEmoteCode, sizeValue, fileExtension)
		outputPath := filepath.Join(emoteFolder, outputFilename)

		outputFile, err := os.Create(outputPath)
		if err != nil {
			fmt.Printf("[skip] %s (cannot create file: %v)\n", outputPath, err)
			response.Body.Close()
			continue
		}

		_, copyError := io.Copy(outputFile, response.Body)
		outputFile.Close()
		response.Body.Close()

		if copyError != nil {
			fmt.Printf("[skip] %s (copy error: %v)\n", outputPath, copyError)
			continue
		}

		fmt.Printf("[ok] %s\n", outputFilename)
	}
}

func downloadChannelEmotes(httpClient *http.Client, channelID string) error {
	channelURL := fmt.Sprintf("%s/channels/%s", twitchemotesBaseURL, channelID)

	document, response, err := fetchDocument(httpClient, channelURL)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("request failed with status %s", response.Status)
	}

	channelDisplayName := getChannelDisplayName(document)
	safeChannelName := makeSafeName(channelDisplayName)
	if safeChannelName == "unknown" {
		safeChannelName = makeSafeName(channelID)
	}
	outputRoot := safeChannelName

	err = os.MkdirAll(outputRoot, 0o755)
	if err != nil {
		return fmt.Errorf("cannot create output directory %s: %w", outputRoot, err)
	}

	fmt.Printf("Channel ID: %s\n", channelID)
	if channelDisplayName != "" {
		fmt.Printf("Channel Name: %s\n", channelDisplayName)
	}
	fmt.Printf("Output Folder: %s\n", outputRoot)
	fmt.Println("Collecting emote metadata...")

	emoteMap := collectEmoteMetadata(document)
	fmt.Printf("Found %d emotes\n", len(emoteMap))

	if len(emoteMap) == 0 {
		return nil
	}

	for emoteIdentifier, emoteData := range emoteMap {
		fmt.Printf("Downloading sizes for emote: %s (%s)\n", emoteData.EmoteCode, emoteIdentifier)
		downloadEmoteImages(httpClient, emoteIdentifier, emoteData, outputRoot)
	}

	return nil
}

func main() {
	var channelIdentifier string

	if len(os.Args) >= 2 {
		channelIdentifier = strings.TrimSpace(os.Args[1])
	} else {
		line, err := readStdinLine("Enter Twitch channel name or numeric ID: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
			os.Exit(1)
		}
		channelIdentifier = line
	}

	if channelIdentifier == "" {
		fmt.Fprintln(os.Stderr, "No channel identifier provided.")
		os.Exit(1)
	}

	httpClient := createHTTPClient()

	channelID, err := resolveChannelIdentifierToID(httpClient, channelIdentifier)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving channel: %v\n", err)
		os.Exit(1)
	}

	err = downloadChannelEmotes(httpClient, channelID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading emotes: %v\n", err)
		os.Exit(1)
	}
}
