package vsco

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/SilverMight/vsco-get/httpclient"
	"github.com/schollz/progressbar/v3"
)

const databaseFile = "vscoget-user-database.txt"

var client = httpclient.NewClient()

type sitesResponse struct {
	Sites []struct {
		ID            int    `json:"id"`
		Profile_image string `json:"profile_image"`
	} `json:"sites"`
}

type Media struct {
	ID              string `json:"_id"`
	Is_video        bool   `json:"is_video"`
	Video_url       string `json:"video_url,omitempty"`
	Responsive_url  string `json:"responsive_url"`
	Upload_date     int64  `json:"upload_date"`
	Perma_subdomain string `json:"perma_subdomain"`
}

type MediaResponse struct {
	Media []Media `json:"media"`
}

type imageList struct {
	Media []Media `json:"media"`
	Total int     `json:"total"`
}

type Scraper struct {
	username     string
	numWorkers   int
	id          int
	profileImage string
}

const (
	PageSize   = 30
	retryCount = 3
	retryDelay = 2 * time.Second
)

func (scraper *Scraper) ID() string {
    return fmt.Sprintf("%d", scraper.id)
}

func GetUsernameFromSiteID(siteID string) (string, error) {
	url := fmt.Sprintf("https://vsco.co/api/2.0/medias?site_id=%s&size=1", siteID)
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var mediaResp MediaResponse
	if err := json.Unmarshal(bodyBytes, &mediaResp); err != nil {
		return "", fmt.Errorf("failed to decode JSON: %w", err)
	}

	if len(mediaResp.Media) == 0 {
		return "", fmt.Errorf("no media found for site ID %s", siteID)
	}

	if mediaResp.Media[0].Perma_subdomain == "" {
		return "", fmt.Errorf("empty perma_subdomain in response")
	}

	return mediaResp.Media[0].Perma_subdomain, nil
}

func GetInfoFromMediaID(mediaID string) (string, string, error) {
    mediaUrl := fmt.Sprintf("https://vsco.co/vsco/media/%s", mediaID)
    req, err := http.NewRequest("GET", mediaUrl, nil)
    if err != nil {
        return "", "", fmt.Errorf("failed to create request: %w", err)
    }

    req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:125.0) Gecko/20100101 Firefox/125.0")
    req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")

    client := http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return "", "", fmt.Errorf("failed to make request: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return "", "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
    }

    bodyBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        return "", "", fmt.Errorf("failed to read response body: %w", err)
    }

    // Search for permaSubdomain in the HTML
    htmlContent := string(bodyBytes)
    permaSubdomain := extractPermaSubdomain(htmlContent)
    if permaSubdomain == "" {
        return "", "", fmt.Errorf("could not find permaSubdomain in HTML")
    }

    fmt.Printf("Found permaSubdomain: %s\n", permaSubdomain)

    siteID, err := GetSiteIDFromUsername(permaSubdomain)
    if err != nil {
        return "", "", fmt.Errorf("failed to get site ID: %w", err)
    }

    return permaSubdomain, siteID, nil
}

func extractPermaSubdomain(html string) string {
    // Look for the permaSubdomain pattern in the HTML
    prefix := `"permaSubdomain":"`
    start := strings.Index(html, prefix)
    if start == -1 {
        return ""
    }
    start += len(prefix)
    end := strings.Index(html[start:], `"`)
    if end == -1 {
        return ""
    }
    return html[start : start+end]
}

func GetSiteIDFromUsername(username string) (string, error) {
	url := fmt.Sprintf("https://vsco.co/api/2.0/sites?subdomain=%s", username)
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var body sitesResponse
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return "", fmt.Errorf("failed to decode JSON: %w", err)
	}

	if len(body.Sites) == 0 {
		return "", fmt.Errorf("no site found for username %s", username)
	}

	return fmt.Sprintf("%d", body.Sites[0].ID), nil
}

func UpdateDatabaseWithSiteID(username, siteID string) error {
	file, err := os.OpenFile(databaseFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open database file: %w", err)
	}
	defer file.Close()

	entry := fmt.Sprintf("%s | %s\n", username, siteID)
	if _, err := file.WriteString(entry); err != nil {
		return fmt.Errorf("failed to write to database: %w", err)
	}

	return nil
}

func updateDatabase(username string, siteID int, status string) error {
	file, err := os.ReadFile(databaseFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read database: %w", err)
	}

	lines := strings.Split(string(file), "\n")
	newLines := make([]string, 0, len(lines)+1)
	found := false

	newEntry := fmt.Sprintf("%s | %d", username, siteID)
	if status != "" {
		newEntry += fmt.Sprintf(" | %s", status)
	}

	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) > 0 && strings.TrimSpace(parts[0]) == username {
			newLines = append(newLines, newEntry)
			found = true
		} else if line != "" {
			newLines = append(newLines, line)
		}
	}

	if !found {
		newLines = append(newLines, newEntry)
	}

	return os.WriteFile(databaseFile, []byte(strings.Join(newLines, "\n")), 0644)
}

func NewScraper(username string, numWorkers int) *Scraper {
	return &Scraper{
		username:   username,
		numWorkers: numWorkers,
	}
}

func (scraper *Scraper) GetUserInfo() (bool, error) {
    url := fmt.Sprintf("https://vsco.co/api/2.0/sites?subdomain=%s", scraper.username)
    resp, err := client.Get(url)
    if err != nil {
        return false, fmt.Errorf("failed to make request: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusNotFound {
        return false, nil // User not found, but not an error
    }

    if resp.StatusCode != http.StatusOK {
        return false, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
    }

    bodyBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        return false, fmt.Errorf("failed to read response body: %w", err)
    }

    var body sitesResponse
    if err := json.Unmarshal(bodyBytes, &body); err != nil {
        return false, fmt.Errorf("failed to decode JSON: %w", err)
    }

    if len(body.Sites) == 0 {
        return false, nil // User not found, but not an error
    }

    scraper.id = body.Sites[0].ID
    scraper.profileImage = body.Sites[0].Profile_image
    return true, nil // User found
}

func (scraper *Scraper) fetchMedia() ([]Media, error) {
	var allMedia []Media

	for page := 0; ; page++ {
		log.Printf("Fetching page %d...", page+1)
		url := fmt.Sprintf("https://vsco.co/api/2.0/medias?site_id=%d&size=%d&page=%d", scraper.id, PageSize, page)
		
		var resp *http.Response
		var err error
		
		for i := 0; i < retryCount; i++ {
			resp, err = client.Get(url)
			if err == nil && resp.StatusCode == http.StatusOK {
				break
			}
			if i < retryCount-1 {
				time.Sleep(retryDelay)
			}
		}
		
		if err != nil {
			return nil, fmt.Errorf("failed to get media list: %w", err)
		}

		var mediaResp MediaResponse
		if err := json.NewDecoder(resp.Body).Decode(&mediaResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode media response: %w", err)
		}
		resp.Body.Close()

		allMedia = append(allMedia, mediaResp.Media...)
		log.Printf("Page %d: Found %d media items (Total: %d)", page+1, len(mediaResp.Media), len(allMedia))

		if len(mediaResp.Media) < PageSize {
			break
		}
	}

	return allMedia, nil
}

func getMediaUrl(media Media) string {
	if media.Is_video && media.Video_url != "" {
		if !strings.HasPrefix(media.Video_url, "http") {
			return "https://" + media.Video_url
		}
		return media.Video_url
	}
	
	if !strings.HasPrefix(media.Responsive_url, "http") {
		return "https://" + media.Responsive_url
	}
	return media.Responsive_url
}

func getMediaFilename(media Media) (string, error) {
    return media.ID, nil
}

func SaveMediaToFile(media Media, folderPath string) error {
    mediaUrl := getMediaUrl(media)
    parsed, err := url.Parse(mediaUrl)
    if err != nil {
        return fmt.Errorf("failed to parse media URL: %w", err)
    }

    fileExt := path.Ext(parsed.Path)
    if fileExt == "" {
        if media.Is_video {
            fileExt = ".mp4"
        } else {
            fileExt = ".jpg"
        }
    }
    
    filename := media.ID + fileExt
    filePath := path.Join(folderPath, filename)

    err = client.DownloadFile(mediaUrl, filePath)
    if err != nil {
        return fmt.Errorf("failed to download %s: %w", mediaUrl, err)
    }

    uploadTime := time.Unix(media.Upload_date/1000, 0)
    os.Chtimes(filePath, uploadTime, uploadTime)

    return nil
}

func createUserDirectory(username string) (string, error) {
	userPath := path.Join(".", username)
	if err := os.MkdirAll(userPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}
	return userPath, nil
}

func (scraper *Scraper) SaveAllMedia() error {
    mediaList, err := scraper.fetchMedia()
    if err != nil {
        return err
    }

    userPath, err := createUserDirectory(scraper.username)
    if err != nil {
        return err
    }

    // Filter out existing files
    filteredMedia, err := stripExistingMedia(mediaList, userPath)
    if err != nil {
        return err
    }

    var wg sync.WaitGroup
    sem := make(chan struct{}, scraper.numWorkers)
    bar := progressbar.Default(int64(len(filteredMedia)), 
        fmt.Sprintf("Downloading new media for %s...", scraper.username))

    for _, media := range filteredMedia {
        wg.Add(1)
        sem <- struct{}{}

        go func(m Media) {
            defer func() {
                <-sem
                wg.Done()
                bar.Add(1)
            }()

            if err := SaveMediaToFile(m, userPath); err != nil {
                log.Printf("Error downloading %s: %v", m.ID, err)
            }
        }(media)
    }

    wg.Wait()
    return nil
}

func (scraper *Scraper) SaveProfilePicture() error {
	userPath, err := createUserDirectory(scraper.username)
	if err != nil {
		return err
	}

	profilePath := path.Join(userPath, "profile")
	if err := os.MkdirAll(profilePath, 0755); err != nil {
		return fmt.Errorf("failed to create profile directory: %w", err)
	}

	u, err := url.Parse(scraper.profileImage)
	if err != nil {
		return fmt.Errorf("failed to parse profile image URL: %w", err)
	}

	q := u.Query()
	q.Del("w")
	q.Del("h")
	u.RawQuery = q.Encode()

	filePath := path.Join(profilePath, fmt.Sprintf("%s.jpg", scraper.username))
	return client.DownloadFile(u.String(), filePath)
}

func stripExistingMedia(mediaList []Media, userPath string) ([]Media, error) {
    var strippedList []Media
    existingFiles := make(map[string]bool)

    // First check the main directory
    files, err := os.ReadDir(userPath)
    if err == nil {
        for _, file := range files {
            if !file.IsDir() {
                // Get base filename without extension
                baseName := strings.TrimSuffix(file.Name(), path.Ext(file.Name()))
                existingFiles[baseName] = true
            }
        }
    }

    // Then check the profile subdirectory if it exists
    profilePath := path.Join(userPath, "profile")
    profileFiles, err := os.ReadDir(profilePath)
    if err == nil {
        for _, file := range profileFiles {
            if !file.IsDir() {
                baseName := strings.TrimSuffix(file.Name(), path.Ext(file.Name()))
                existingFiles[baseName] = true
            }
        }
    }

    // Filter out existing media
    for _, media := range mediaList {
        if !existingFiles[media.ID] {
            strippedList = append(strippedList, media)
        }
    }

    log.Printf("Filtered %d existing files, %d new files to download", 
        len(mediaList)-len(strippedList), len(strippedList))
    
    return strippedList, nil
}

func GetMediaFromUserlist(listPath string, numWorkers int, getProfilePictures bool) error {
	file, err := os.Open(listPath)
	if err != nil {
		return fmt.Errorf("failed to open userlist: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		username := strings.TrimSpace(scanner.Text())
		if username == "" {
			continue
		}

		scraper := NewScraper(username, numWorkers)
		found, err := scraper.GetUserInfo()
		if err != nil {
			log.Printf("Error getting user info for %s: %v", username, err)
			continue
		}
		if !found {
			log.Printf("User %s not found", username)
			continue
		}

		if getProfilePictures {
			if err := scraper.SaveProfilePicture(); err != nil {
				log.Printf("Error saving profile picture for %s: %v", username, err)
			}
		} else {
			if err := scraper.SaveAllMedia(); err != nil {
				log.Printf("Error saving media for %s: %v", username, err)
			}
		}
	}

	return scanner.Err()
}
