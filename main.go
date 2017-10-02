package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bitrise-io/go-utils/log"
)

type ConfigModel struct {
	GitHubAuthToken   string
	RepositoryURL     string
	ChangelogFileList string
	ReleaseTag        string
	ReleaseName       string
	TargetCommitish   string
	IsDraft           bool
	IsPrerelease      bool
	UploadAssetFile   string
}

type GitHubApiConfig struct {
	User      string
	Repo      string
	AuthToken string
}

type GitHubRelease struct {
	ID              int    `json:"id,omitempty"`
	TagName         string `json:"tag_name,omitempty"`
	Name            string `json:"name,omitempty"`
	TargetCommitish string `json:"target_commitish,omitempty"`
	Body            string `json:"body,omitempty"`
	Draft           bool   `json:"draft,omitempty"`
	Prerelease      bool   `json:"prerelease,omitempty"`
	UploadURL       string `json:"upload_url,omitempty"`
	HTMLURL         string `json:"html_url,omitempty"`
}

var gitAPIRegexp = regexp.MustCompile(`([A-Za-z0-9]+@|http(|s)\:\/\/)([A-Za-z0-9.-]+)(:|\/)([^.]+)\/([^.]+)(\.git)?`)

const gitHubBaseURL = "https://api.github.com"
const gitHubUploadURL = "https://uploads.github.com"
const defaultMediaType = "application/octet-stream"

func createConfigsModelFromEnvs() ConfigModel {
	return ConfigModel{
		GitHubAuthToken:   os.Getenv("github_auth_token"),
		RepositoryURL:     os.Getenv("repository_url"),
		ChangelogFileList: os.Getenv("changelog_file_list"),
		ReleaseTag:        os.Getenv("release_tag"),
		ReleaseName:       os.Getenv("release_name"),
		TargetCommitish:   os.Getenv("target_commitish"),
		IsDraft:           os.Getenv("is_draft") == "true",
		IsPrerelease:      os.Getenv("is_prerelease") == "true",
		UploadAssetFile:   os.Getenv("upload_asset_file"),
	}
}

func (configs ConfigModel) print() {
	log.Infof("Configs:")
	log.Printf("- GitHubAuthToken: %s", configs.GitHubAuthToken)
	log.Printf("- RepositoryURL: %s", configs.RepositoryURL)
	log.Printf("- ChangelogFileList: %s", configs.ChangelogFileList)
	log.Printf("- ReleaseTag: %s", configs.ReleaseTag)
	log.Printf("- ReleaseName: %s", configs.ReleaseName)
	log.Printf("- TargetCommitish: %s", configs.TargetCommitish)
	log.Printf("- IsDraft: %v", configs.IsDraft)
	log.Printf("- IsPrerelease: %v", configs.IsPrerelease)
	log.Printf("- UploadAssetFile: %v", configs.UploadAssetFile)
}

func (apiConfig GitHubApiConfig) print() {
	log.Infof("ApiConfig:")
	log.Printf("- User: %s", apiConfig.User)
	log.Printf("- Repository: %s", apiConfig.Repo)
	log.Printf("- AuthToken: %s", apiConfig.AuthToken)
}

func (apiConfig GitHubApiConfig) getCreateReleasesURL() string {
	return gitHubBaseURL +
		"/repos/" + apiConfig.User + "/" + apiConfig.Repo +
		"/releases?access_token=" + apiConfig.AuthToken
}

func (apiConfig GitHubApiConfig) getUploadAssetURL(releaseId int, name string) string {
	return gitHubUploadURL +
		"/repos/" + apiConfig.User + "/" + apiConfig.Repo +
		"/releases/" + strconv.Itoa(releaseId) + "/assets?access_token=" + apiConfig.AuthToken +
		"&name=" + name
}

func inferGithubAPIConfig(config ConfigModel) (GitHubApiConfig, error) {
	var apiConf GitHubApiConfig

	match := gitAPIRegexp.FindStringSubmatch(config.RepositoryURL)
	if len(match) < 7 {
		return apiConf, errors.New("error: User and Repo could not be obtained")
	}

	apiConf = GitHubApiConfig{
		User:      match[5],
		Repo:      match[6],
		AuthToken: config.GitHubAuthToken,
	}
	return apiConf, nil
}

func collectReleaseNotes(files string) string {
	var buffer bytes.Buffer

	for i, item := range strings.Split(files, "|") {
		fileContent, err := ioutil.ReadFile(strings.TrimSpace(item))
		if err != nil {
			log.Errorf("%v", err)
			continue
		}

		if i > 0 {
			buffer.WriteString("\n\n")
		}

		buffer.Write(fileContent)
	}

	return buffer.String()
}

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)
	os.Exit(1)
}

func createRelease(config ConfigModel, releaseNotes string) GitHubRelease {
	return GitHubRelease{
		Name:            config.ReleaseName,
		TagName:         config.ReleaseTag,
		Draft:           config.IsDraft,
		Prerelease:      config.IsPrerelease,
		TargetCommitish: config.TargetCommitish,
		Body:            releaseNotes,
	}
}

func postAsset(apiConf GitHubApiConfig, release *GitHubRelease, postAsset *os.File) error {
	defer postAsset.Close()

	stat, err := postAsset.Stat()
	if err != nil {
		return err
	}

	if stat.IsDir() {
		return errors.New("asset can't be a directory")
	}

	uploadURL := apiConf.getUploadAssetURL(release.ID, filepath.Base(postAsset.Name()))
	mediaType := mime.TypeByExtension(filepath.Ext(postAsset.Name()))
	if mediaType == "" {
		mediaType = defaultMediaType
	}

	log.Infof("Posting asset to %s", uploadURL)

	hc := http.Client{}
	req, err := http.NewRequest("POST", uploadURL, postAsset)
	if err != nil {
		return err
	}

	req.ContentLength = stat.Size()
	req.Header.Set("Content-Type", mediaType)
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != 201 {
		return errors.New("fileupload failed with " + resp.Status)
	}

	return nil
}

func postRelease(url string, release *GitHubRelease) error {
	jsonRelease, err := json.Marshal(release)
	if err != nil {
		return err
	}
	log.Printf(string(jsonRelease))

	log.Infof("Posting Release to: %v", url)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonRelease))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		return errors.New("GitHub API could not create release")
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	err = json.Unmarshal(body, &release)
	if err != nil {
		return err
	}

	return nil
}

func main() {

	config := createConfigsModelFromEnvs()
	config.print()

	gitHubAPIConfig, err := inferGithubAPIConfig(config)
	if err != nil {
		failf("Failed to infer GitHub API config")
	}
	gitHubAPIConfig.print()

	releaseNotes := collectReleaseNotes(config.ChangelogFileList)

	release := createRelease(config, releaseNotes)
	err = postRelease(gitHubAPIConfig.getCreateReleasesURL(), &release)
	if err != nil {
		failf("Failed to create Github release entry with error: %v", err)
	}

	if config.UploadAssetFile != "" {
		uploadFile, err := os.Open(config.UploadAssetFile)
		if err != nil {
			log.Errorf("%v", err)
		} else {
			err = postAsset(gitHubAPIConfig, &release, uploadFile)
			if err != nil {
				log.Errorf("%v", err)
			}
		}
	}

	cmdLog, err := exec.Command("bitrise", "envman", "add", "--key", "RELEASE_URL", "--value", release.HTMLURL).CombinedOutput()
	if err != nil {
		failf("Failed to expose output with envman, error: %#v | output: %s", err, cmdLog)
	}

}
