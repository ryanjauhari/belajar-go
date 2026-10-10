package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var client = &http.Client{Timeout: 15 * time.Second}

const userAgent = "TelegramWrapper/1.0"

// PostJSON mengirim payload JSON ke url via POST. Bila gagal, dicoba ulang
// beberapa kali dengan jeda singkat.
func PostJSON(url string, payload interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode callback payload: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		lastErr = postOnce(url, body)
		if lastErr == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return lastErr
}

func postOnce(url string, body []byte) error {
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create callback request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	// User-Agent eksplisit supaya tidak diblokir sebagian proxy/CDN.
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send callback: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("callback returned HTTP %d", response.StatusCode)
	}
	return nil
}
