package main

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"gator/internal/database"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type RSSFeed struct {
	Channel struct {
		Title       string    `xml:"title"`
		Link        string    `xml:"link"`
		Description string    `xml:"description"`
		Item        []RSSItem `xml:"item"`
	} `xml:"channel"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

// fetchFeed handles fetching an XML payload over HTTP and decoding it into an RSSFeed structure.
func fetchFeed(ctx context.Context, feedURL string) (*RSSFeed, error) {
	// 1. Prepare an HTTP GET request carrying our operational Context
	req, err := http.NewRequestWithContext(ctx, "GET", feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Identify our application to the remote server
	req.Header.Set("User-Agent", "gator")

	// 2. Execute the network request using the default HTTP Client
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request execution failed: %w", err)
	}
	defer resp.Body.Close()

	// Intercept bad HTTP status responses early
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server responded with status code: %d", resp.StatusCode)
	}

	// 3. Read raw body binary stream into a byte slice
	rawXML, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	// 4. Decode the byte array contents into our struct layout
	var feed RSSFeed
	err = xml.Unmarshal(rawXML, &feed)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal xml: %w", err)
	}

	// 5. Clean up HTML entities across human-readable text properties
	feed.Channel.Title = html.UnescapeString(feed.Channel.Title)
	feed.Channel.Description = html.UnescapeString(feed.Channel.Description)

	for i := range feed.Channel.Item {
		feed.Channel.Item[i].Title = html.UnescapeString(feed.Channel.Item[i].Title)
		feed.Channel.Item[i].Description = html.UnescapeString(feed.Channel.Item[i].Description)
	}

	return &feed, nil
}

func parsePublishedAt(pubDate string) sql.NullTime {
	if pubDate == "" {
		return sql.NullTime{}
	}

	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
		time.RFC3339Nano,
		"Mon, _2 Jan 2006 15:04:05 MST", // Common custom variants
	}

	for _, format := range formats {
		if t, err := time.Parse(format, pubDate); err == nil {
			return sql.NullTime{Time: t.UTC(), Valid: true}
		}
	}
	return sql.NullTime{}
}

// scrapeFeeds looks up the oldest pending feed record, marks it as fetched,
// and retrieves its active web items to output back to the console.
func scrapeFeeds(s *state) {
	ctx := context.Background()

	nextFeed, err := s.db.GetNextFeedToFetch(ctx)
	if err != nil {
		fmt.Printf("Worker Error: %v\n", err)
		return
	}

	now := time.Now().UTC()
	_, err = s.db.MarkFeedFetched(ctx, database.MarkFeedFetchedParams{
		ID:            nextFeed.ID,
		LastFetchedAt: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		fmt.Printf("Worker Error marking fetched: %v\n", err)
		return
	}

	fmt.Printf("\nScraping target: %s...\n", nextFeed.Name)
	feedData, err := fetchFeed(ctx, nextFeed.Url)
	if err != nil {
		fmt.Printf("Worker Error fetching feed: %v\n", err)
		return
	}

	savedCount := 0
	for _, item := range feedData.Channel.Item {
		// Clean up description nullability
		var desc sql.NullString
		if item.Description != "" {
			desc = sql.NullString{String: item.Description, Valid: true}
		}

		publishedAt := parsePublishedAt(item.PubDate)

		_, err = s.db.CreatePost(ctx, database.CreatePostParams{
			ID:          uuid.New(),
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
			Title:       item.Title,
			Url:         item.Link,
			Description: desc,
			PublishedAt: publishedAt,
			FeedID:      nextFeed.ID,
		})
		if err != nil {
			// Catch unique constraint violations for URLs (pq error code 23505)
			if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "23505") {
				continue // Skip duplicates silently
			}
			fmt.Printf("Error saving post '%s': %v\n", item.Title, err)
			continue
		}
		savedCount++
	}
	fmt.Printf("Finished scraping! Saved %d new posts from '%s'.\n", savedCount, nextFeed.Name)
}
