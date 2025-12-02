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
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	twitchemotesBaseURL  = "https://twitchemotes.com"
	defaultUserAgent     = "Mozilla/5.0 (X11; Linux x86_64) twe-dlp/1.0"
	httpRequestTimeout   = 30 * time.Second
	logBufferMaxMessages = 200
)

var (
	emoteSizeList     = []string{"1.0", "2.0", "3.0"}
	channelURLPattern = regexp.MustCompile(`/channels/(\d+)`)
	htmlTagPattern    = regexp.MustCompile(`<.*?>`)
	safeNamePattern   = regexp.MustCompile(`[^A-Za-z0-9_]+`)
)

type EmoteData struct {
	BaseURL    string
	FormatType string
	EmoteCode  string
}

type downloadResultMessage struct {
	Error    error
	LogLines []string
}

type model struct {
	textInput         textinput.Model
	logLines          []string
	downloading       bool
	downloadError     error
	httpClient        *http.Client
	showHelp          bool
	styleTitle        lipgloss.Style
	styleLogPlain     lipgloss.Style
	styleLogOK        lipgloss.Style
	styleLogSkip      lipgloss.Style
	styleLogError     lipgloss.Style
	styleHelpBoxTitle lipgloss.Style
	styleHelpBoxBody  lipgloss.Style
	styleFooter       lipgloss.Style
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

func downloadEmoteImages(httpClient *http.Client, emoteIdentifier string, emoteData EmoteData, outputRoot string, logFunc func(string)) {
	emoteCode := emoteData.EmoteCode
	emoteBaseURL := emoteData.BaseURL

	safeEmoteCode := makeSafeName(emoteCode)
	emoteFolder := filepath.Join(outputRoot, safeEmoteCode)
	err := os.MkdirAll(emoteFolder, 0o755)
	if err != nil {
		logFunc(fmt.Sprintf("[error] cannot create folder %s: %v", emoteFolder, err))
		return
	}

	for _, sizeValue := range emoteSizeList {
		imageURL := fmt.Sprintf("%s/light/%s", emoteBaseURL, sizeValue)

		request, err := http.NewRequest("GET", imageURL, nil)
		if err != nil {
			logFunc(fmt.Sprintf("[skip] %s (%v)", imageURL, err))
			continue
		}
		request.Header.Set("User-Agent", defaultUserAgent)

		response, err := httpClient.Do(request)
		if err != nil {
			logFunc(fmt.Sprintf("[skip] %s (%v)", imageURL, err))
			continue
		}

		if response.StatusCode != http.StatusOK {
			logFunc(fmt.Sprintf("[skip] %s (status %s)", imageURL, response.Status))
			response.Body.Close()
			continue
		}

		contentType := response.Header.Get("Content-Type")
		fileExtension := determineFileExtension(contentType)
		outputFilename := fmt.Sprintf("%s_%s.%s", safeEmoteCode, sizeValue, fileExtension)
		outputPath := filepath.Join(emoteFolder, outputFilename)

		outputFile, err := os.Create(outputPath)
		if err != nil {
			logFunc(fmt.Sprintf("[skip] %s (cannot create file: %v)", outputPath, err))
			response.Body.Close()
			continue
		}

		_, copyError := io.Copy(outputFile, response.Body)
		outputFile.Close()
		response.Body.Close()

		if copyError != nil {
			logFunc(fmt.Sprintf("[skip] %s (copy error: %v)", outputPath, copyError))
			continue
		}

		logFunc(fmt.Sprintf("[ok] %s", outputFilename))
	}
}

func downloadChannelEmotes(httpClient *http.Client, channelID string, logFunc func(string)) error {
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

	logFunc(fmt.Sprintf("Channel ID: %s", channelID))
	if channelDisplayName != "" {
		logFunc(fmt.Sprintf("Channel Name: %s", channelDisplayName))
	}
	logFunc(fmt.Sprintf("Output Folder: %s", outputRoot))
	logFunc("Collecting emote metadata...")

	emoteMap := collectEmoteMetadata(document)
	logFunc(fmt.Sprintf("Found %d emotes", len(emoteMap)))

	if len(emoteMap) == 0 {
		return nil
	}

	for emoteIdentifier, emoteData := range emoteMap {
		logFunc(fmt.Sprintf("Downloading sizes for emote: %s (%s)", emoteData.EmoteCode, emoteIdentifier))
		downloadEmoteImages(httpClient, emoteIdentifier, emoteData, outputRoot, logFunc)
	}

	return nil
}

func newModel(httpClient *http.Client) model {
	input := textinput.New()
	input.Placeholder = ""
	input.Focus()
	input.Prompt = "> "
	input.CharLimit = 128

	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#11111b")).
		Background(lipgloss.Color("#f5c2e7")).
		Bold(true).
		Padding(0, 1)

	logPlain := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#cdd6f4"))

	logOK := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#a6e3a1"))

	logSkip := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f9e2af"))

	logError := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f38ba8"))

	helpTitle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f5c2e7")).
		Bold(true)

	helpBody := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#cdd6f4"))

	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#585b70")).
		PaddingTop(1)

	return model{
		textInput:         input,
		logLines:          []string{},
		httpClient:        httpClient,
		showHelp:          false,
		styleTitle:        title,
		styleLogPlain:     logPlain,
		styleLogOK:        logOK,
		styleLogSkip:      logSkip,
		styleLogError:     logError,
		styleHelpBoxTitle: helpTitle,
		styleHelpBoxBody:  helpBody,
		styleFooter:       footer,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) hasExactLogLine(line string) bool {
	for _, existing := range m.logLines {
		if existing == line {
			return true
		}
	}
	return false
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		default:
		}

		if msg.String() == "q" {
			return m, tea.Quit
		}

		if msg.String() == "?" {
			m.showHelp = !m.showHelp
			return m, nil
		}

		if msg.Type == tea.KeyEnter {
			if m.downloading {
				return m, nil
			}

			channelIdentifier := strings.TrimSpace(m.textInput.Value())
			if channelIdentifier == "" {
				warningText := "Please enter a Twitch channel name or ID."
				if !m.hasExactLogLine(warningText) {
					m.appendLogLine(warningText)
				}
				return m, nil
			}

			m.downloading = true
			m.downloadError = nil
			m.appendLogLine(fmt.Sprintf("Resolving channel %q...", channelIdentifier))

			return m, func() tea.Msg {
				collectedLogs := make([]string, 0, 64)
				logFunc := func(line string) {
					collectedLogs = append(collectedLogs, line)
				}

				channelID, err := resolveChannelIdentifierToID(m.httpClient, channelIdentifier)
				if err != nil {
					logFunc(fmt.Sprintf("Error resolving channel: %v", err))
					return downloadResultMessage{
						Error:    err,
						LogLines: collectedLogs,
					}
				}

				err = downloadChannelEmotes(m.httpClient, channelID, logFunc)

				return downloadResultMessage{
					Error:    err,
					LogLines: collectedLogs,
				}
			}
		}

		if !m.downloading {
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(msg)
			return m, cmd
		}
		return m, nil

	case downloadResultMessage:
		for _, line := range msg.LogLines {
			m.appendLogLine(line)
		}
		if msg.Error != nil {
			m.appendLogLine(fmt.Sprintf("Error: %v", msg.Error))
			m.downloadError = msg.Error
		} else {
			m.appendLogLine("Download completed.")
		}
		m.downloading = false
		m.textInput.SetValue("")
		m.textInput.Focus()
		return m, nil

	default:
		if !m.downloading {
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(message)
			return m, cmd
		}
	}

	return m, nil
}

func (m *model) appendLogLine(line string) {
	if line == "" {
		return
	}
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > logBufferMaxMessages {
		m.logLines = m.logLines[len(m.logLines)-logBufferMaxMessages:]
	}
}

func (m model) renderHelpBox() string {
	var builder strings.Builder

	builder.WriteString(m.styleHelpBoxTitle.Render("Usage"))
	builder.WriteString("\n")
	builder.WriteString(m.styleHelpBoxBody.Render("  tw-dlp <channel|id>"))

	return builder.String()
}

func (m model) View() string {
	var builder strings.Builder

	builder.WriteString(m.styleTitle.Render(" tw-dlp "))
	builder.WriteString("\n\n")

	if m.showHelp {
		builder.WriteString(m.renderHelpBox())
		builder.WriteString("\n\n")
	}

	if len(m.logLines) > 0 {
		for _, line := range m.logLines {
			var styledLine string
			switch {
			case strings.HasPrefix(line, "[ok]"):
				styledLine = m.styleLogOK.Render(line)
			case strings.HasPrefix(line, "[skip]"):
				styledLine = m.styleLogSkip.Render(line)
			case strings.HasPrefix(line, "[error]"), strings.HasPrefix(line, "Error:"):
				styledLine = m.styleLogError.Render(line)
			default:
				styledLine = m.styleLogPlain.Render(line)
			}
			builder.WriteString("  ")
			builder.WriteString(styledLine)
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
	}

	builder.WriteString(m.textInput.View())
	builder.WriteString("\n")

	footerText := "Esc/q: quit • ? more"
	builder.WriteString(m.styleFooter.Render(footerText))
	builder.WriteString("\n")

	return builder.String()
}

func runTextMode(httpClient *http.Client, channelIdentifier string) int {
	channelID, err := resolveChannelIdentifierToID(httpClient, channelIdentifier)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving channel: %v\n", err)
		return 1
	}

	logFunc := func(line string) {
		fmt.Println(line)
	}

	err = downloadChannelEmotes(httpClient, channelID, logFunc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading emotes: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	httpClient := createHTTPClient()

	if len(os.Args) >= 2 {
		channelIdentifier := strings.TrimSpace(os.Args[1])
		if channelIdentifier == "" {
			fmt.Fprintln(os.Stderr, "No channel identifier provided.")
			os.Exit(1)
		}
		exitCode := runTextMode(httpClient, channelIdentifier)
		os.Exit(exitCode)
	}

	initialModel := newModel(httpClient)
	if _, err := tea.NewProgram(initialModel).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}
