package ipfetcher

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// GetIP fetches the public IP address from icanhazip.com
func GetIP() (string, error) {
	// Create a custom HTTP client with timeout settings
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}

	// Make the GET request
	resp, err := client.Get("https://icanhazip.com")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Trim any surrounding whitespace from the response body
	ip := strings.TrimSpace(string(body))
	return ip, nil
}
