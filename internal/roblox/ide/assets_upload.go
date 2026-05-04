package ide

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

var (
	ErrRateLimited = fmt.Errorf("rate limited")
	ErrBadRequest  = fmt.Errorf("bad request")
	ErrEmptyFile   = fmt.Errorf("file content is empty")
)

// setAPIKeyHeader adds the Open Cloud API key header.
func setAPIKeyHeader(req *http.Request, apiKey string) {
	req.Header.Set("x-api-key", apiKey)
}

// assetRequest is the JSON structure for the "request" part of the multipart body.
type assetRequest struct {
	AssetType       string          `json:"assetType"`
	DisplayName     string          `json:"displayName"`
	Description     string          `json:"description"`
	CreationContext *creationContext `json:"creationContext,omitempty"`
}

type creationContext struct {
	GroupID *int64  `json:"groupId,omitempty"`
	Creator *creator `json:"creator,omitempty"`
}

type creator struct {
	UserID int64 `json:"userId"`
}

// UploadAssetUsingOpenCloud uploads an asset via the Open Cloud API.
// userID is required only when groupID is 0 (personal upload).
func UploadAssetUsingOpenCloud(apiKey, assetType, name, description string, fileData []byte, fileName string, groupID int64, userID int64) (string, error) {
	if len(fileData) == 0 {
		return "", fmt.Errorf("%w: asset file is empty (name=%q, type=%q)", ErrEmptyFile, name, assetType)
	}

	// Build the JSON request part
	reqData := assetRequest{
		AssetType:   assetType,
		DisplayName: name,
		Description: description,
	}
	if groupID > 0 {
		reqData.CreationContext = &creationContext{GroupID: &groupID}
	} else {
		reqData.CreationContext = &creationContext{Creator: &creator{UserID: userID}}
	}

	reqJSON, err := json.Marshal(reqData)
	if err != nil {
		return "", fmt.Errorf("marshal asset request: %w", err)
	}

	// Build multipart body
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Part 1: "request"
	reqPart, err := writer.CreateFormField("request")
	if err != nil {
		return "", fmt.Errorf("create request field: %w", err)
	}
	if _, err := reqPart.Write(reqJSON); err != nil {
		return "", fmt.Errorf("write request JSON: %w", err)
	}

	// Part 2: "fileContent"
	filePart, err := writer.CreateFormFile("fileContent", fileName)
	if err != nil {
		return "", fmt.Errorf("create fileContent field: %w", err)
	}
	if _, err := filePart.Write(fileData); err != nil {
		return "", fmt.Errorf("write file data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close writer: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequest("POST", "https://apis.roblox.com/assets/v1/assets", body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	setAPIKeyHeader(req, apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 429 {
		return "", ErrRateLimited
	}

	if resp.StatusCode != 200 {
		if bytes.Contains(respBody, []byte("inappropriate")) {
			return "", fmt.Errorf("inappropriate name or description")
		}
		return "", fmt.Errorf("create asset failed (%d): %s", resp.StatusCode, string(respBody))
	}

	// Parse the operation ID from the response
	var createResult struct {
		OperationID string `json:"operationId"`
	}
	if err := json.Unmarshal(respBody, &createResult); err != nil {
		return "", fmt.Errorf("parse operation id: %w, body: %s", err, string(respBody))
	}

	// Poll for completion
	assetIDStr, err := pollOperation(apiKey, createResult.OperationID)
	if err != nil {
		return "", err
	}
	return assetIDStr, nil
}

func pollOperation(apiKey, operationID string) (string, error) {
	url := fmt.Sprintf("https://apis.roblox.com/assets/v1/operations/%s", operationID)
	client := &http.Client{Timeout: 30 * time.Second}

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

		body, _ := io.ReadAll(resp.Body)

		if resp.StatusCode == 429 {
			return "", ErrRateLimited
		}
		if resp.StatusCode != 200 {
			return "", fmt.Errorf("poll failed (%d): %s", resp.StatusCode, string(body))
		}

		var result struct {
			Done     bool   `json:"done"`
			Response *struct {
				AssetID string `json:"assetId"`
			} `json:"response"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return "", err
		}

		if result.Done {
			if result.Error != nil {
				return "", fmt.Errorf("operation error: %s - %s", result.Error.Code, result.Error.Message)
			}
			if result.Response == nil {
				return "", fmt.Errorf("no asset id in response")
			}
			return result.Response.AssetID, nil
		}

		time.Sleep(2 * time.Second)
	}
}
