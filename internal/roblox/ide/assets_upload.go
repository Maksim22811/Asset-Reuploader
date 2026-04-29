package ide

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

var (
	ErrRateLimited = fmt.Errorf("rate limited")
	ErrBadRequest  = fmt.Errorf("bad request")
)

// setAPIKeyHeader adds the Open Cloud API key header to a request.
func setAPIKeyHeader(req *http.Request, apiKey string) {
	req.Header.Set("x-api-key", apiKey)
}

// newCreateAssetRequest builds a multipart HTTP request for asset creation.
func newCreateAssetRequest(apiKey string, assetType string, name string, description string, fileData []byte, fileName string) (*http.Request, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	_ = writer.WriteField("assetType", assetType)
	_ = writer.WriteField("displayName", name)
	_ = writer.WriteField("description", description)

	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, fmt.Errorf("write file data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close writer: %w", err)
	}

	req, err := http.NewRequest("POST", "https://apis.roblox.com/assets/v1/assets", body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	setAPIKeyHeader(req, apiKey)

	return req, nil
}

// pollOperation repeatedly checks the asset creation operation until it completes.
func pollOperation(client *http.Client, apiKey string, operationID string) (string, error) {
	url := fmt.Sprintf("https://apis.roblox.com/assets/v1/operations/%s", operationID)
	for {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}
		setAPIKeyHeader(req, apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode == 429 {
			return "", ErrRateLimited
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("poll failed (%d): %s", resp.StatusCode, string(body))
		}

		var result struct {
			Done       bool   `json:"done"`
			Response   struct {
				AssetID string `json:"assetId"`
			} `json:"response"`
			Error      struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", err
		}

		if result.Done {
			if result.Error.Code != "" {
				if strings.Contains(result.Error.Message, "inappropriate") {
					return "", fmt.Errorf("inappropriate content")
				}
				return "", fmt.Errorf("operation failed: %s", result.Error.Message)
			}
			return result.Response.AssetID, nil
		}
		time.Sleep(2 * time.Second)
	}
}

// parseAssetID extracts the operation ID from a create response.
func parseAssetID(resp *http.Response) (string, error) {
	var result struct {
		OperationID string `json:"operationId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode operation id: %w", err)
	}
	return result.OperationID, nil
}

// UploadAssetUsingOpenCloud creates an asset via the Open Cloud API and returns its asset ID.
func UploadAssetUsingOpenCloud(apiKey, assetType, name, description string, fileData []byte, fileName string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := newCreateAssetRequest(apiKey, assetType, name, description, fileData, fileName)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return "", ErrRateLimited
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "inappropriate") {
			return "", fmt.Errorf("inappropriate name or description")
		}
		return "", fmt.Errorf("create asset failed (%d): %s", resp.StatusCode, string(body))
	}

	operationID, err := parseAssetID(resp)
	if err != nil {
		return "", err
	}

	assetID, err := pollOperation(client, apiKey, operationID)
	if err != nil {
		return "", err
	}
	return assetID, nil
}
