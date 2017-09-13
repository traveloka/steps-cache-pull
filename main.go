package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/bitrise-io/go-utils/command"
)

var (
	gIsDebugMode = false
)

// StepParamsModel ...
type StepParamsModel struct {
	CacheAPIURL string
	IsDebugMode bool
}

// CreateStepParamsFromEnvs ...
func CreateStepParamsFromEnvs() (StepParamsModel, error) {
	stepParams := StepParamsModel{
		CacheAPIURL: os.Getenv("cache_api_url"),
		IsDebugMode: os.Getenv("is_debug_mode") == "true",
	}

	return stepParams, nil
}

func uncompressCaches(cacheFilePath string) error {
	tarCmdParams := []string{"-xPvf", cacheFilePath}

	if gIsDebugMode {
		log.Printf(" $ tar %s", tarCmdParams)
	}

	cmd := command.New("tar", tarCmdParams...)
	fullOut, err := cmd.RunAndReturnTrimmedCombinedOutput()
	if err != nil {
		log.Printf(" [!] Failed to uncompress cache archive, full output (stdout & stderr) was: %s", fullOut)
		return fmt.Errorf("Failed to uncompress cache archive, error was: %s", err)
	}

	log.Printf(fullOut)

	return nil
}

func downloadFile(url string, localPath string) error {
	out, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("Failed to open the local cache file for write: %s", err)
	}
	defer func() {
		if err := out.Close(); err != nil {
			log.Printf(" [!] Failed to close Archive download file (%s): %s", localPath, err)
		}
	}()

	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("Failed to create cache download request: %s", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf(" [!] Failed to close Archive download response body: %s", err)
		}
	}()

	if resp.StatusCode != 200 {
		responseBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Printf(" (!) Failed to read response body: %s", err)
		}
		log.Printf(" ==> (!) Response content: %s", responseBytes)
		return fmt.Errorf("Failed to download archive - non success response code: %d", resp.StatusCode)
	}

	// Writer the body to file
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("Failed to save cache content into file: %s", err)
	}

	return nil
}

// GenerateDownloadURLRespModel ...
type GenerateDownloadURLRespModel struct {
	DownloadURL string `json:"download_url"`
}

func getCacheDownloadURL(cacheAPIURL string) (string, error) {
	req, err := http.NewRequest("GET", cacheAPIURL, nil)
	if err != nil {
		return "", fmt.Errorf("Failed to create request: %s", err)
	}
	// req.Header.Set("Content-Type", "application/json")
	// req.Header.Set("Api-Token", apiToken)
	// req.Header.Set("X-Bitrise-Event", "hook")

	client := &http.Client{
		Timeout: 20 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Failed to send request: %s", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf(" [!] Exception: Failed to close response body, error: %s", err)
		}
	}()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("Request sent, but failed to read response body (http-code:%d): %s", resp.StatusCode, body)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 202 {
		return "", fmt.Errorf("Build cache not found. Probably cache not initialised yet (first cache push initialises the cache), nothing to worry about ;)")
	}

	var respModel GenerateDownloadURLRespModel
	if err := json.Unmarshal(body, &respModel); err != nil {
		return "", fmt.Errorf("Request sent, but failed to parse JSON response (http-code:%d): %s", resp.StatusCode, body)
	}

	if respModel.DownloadURL == "" {
		return "", fmt.Errorf("Request sent, but Download URL is empty (http-code:%d): %s", resp.StatusCode, body)
	}

	return respModel.DownloadURL, nil
}

func downloadFileWithRetry(cacheAPIURL string, localPath string) error {
	downloadURL, err := getCacheDownloadURL(cacheAPIURL)
	if err != nil {
		return err
	}
	if gIsDebugMode {
		log.Printf("   [DEBUG] downloadURL: %s", downloadURL)
	}

	if err := downloadFile(downloadURL, localPath); err != nil {
		fmt.Println()
		log.Printf(" ===> (!) First download attempt failed, retrying...")
		fmt.Println()
		time.Sleep(3000 * time.Millisecond)
		return downloadFile(downloadURL, localPath)
	}
	return nil
}

func main() {
	log.Println("Cache pull...")

	stepParams, err := CreateStepParamsFromEnvs()
	if err != nil {
		log.Fatalf(" [!] Input error : %s", err)
	}
	gIsDebugMode = stepParams.IsDebugMode
	if gIsDebugMode {
		log.Printf("=> stepParams: %#v", stepParams)
	}
	if stepParams.CacheAPIURL == "" {
		log.Println(" (i) No Cache API URL specified, there's no cache to use, exiting.")
		return
	}

	//
	// Download Cache Archive
	//

	log.Println("=> Downloading Cache ...")
	cacheArchiveFilePath := "/tmp/cache-archive.tar"
	if err := downloadFileWithRetry(stepParams.CacheAPIURL, cacheArchiveFilePath); err != nil {
		log.Fatalf(" [!] Unable to download cache: %s", err)
	}

	if gIsDebugMode {
		log.Printf("=> cacheArchiveFilePath: %s", cacheArchiveFilePath)
	}
	log.Println("=> Downloading Cache [DONE]")

	//
	// Uncompress cache
	//
	log.Println("=> Uncompressing Cache ...")

	log.Println("=> Uncompressing Cache [DONE]")

	log.Println("=> Finished")
}
