package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"time"

	"gopkg.in/tylerb/graceful.v1"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

const (
	githubStatusSquashContext     = "review/squash"
	githubStatusPeerReviewContext = "review/peer"
)

func main() {
	conf := NewConfig()
	githubClient := initGithubClient(conf.AccessToken)
	reposDir, err := ioutil.TempDir("", "github-review-helper")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(reposDir)

	git := NewGit(reposDir)

	mux := http.NewServeMux()
	mux.Handle("/", CreateHandler(conf, git, githubClient.PullRequests, githubClient.Repositories, githubClient.Issues))

	graceful.Run(fmt.Sprintf(":%d", conf.Port), 10*time.Second, mux)
}

func CreateHandler(conf Config, git Git, pullRequests PullRequests, repositories Repositories, issues Issues) Handler {
	return func(w http.ResponseWriter, r *http.Request) Response {
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			return ErrorResponse{err, http.StatusInternalServerError, "Failed to read the request's body"}
		}
		if errResp := checkAuthentication(body, r, conf.Secret); errResp != nil {
			return errResp
		}
		eventType := r.Header.Get("X-Github-Event")
		switch eventType {
		case "issue_comment":
			return handleIssueComment(body, git, pullRequests, repositories, issues)
		case "pull_request":
			return handlePullRequestEvent(body, pullRequests, repositories)
		}
		return SuccessResponse{"Not an event I understand. Ignoring."}
	}
}

// isPlusOneComment matches strings that contain either a +1 (not followed by other digits) or a :+1: emoji
var isPlusOneComment = regexp.MustCompile(`\+1($|\D)`)

func handleIssueComment(body []byte, git Git, pullRequests PullRequests, repositories Repositories, issues Issues) Response {
	issueComment, err := parseIssueComment(body)
	if err != nil {
		return ErrorResponse{err, http.StatusInternalServerError, "Failed to parse the request's body"}
	}
	if !issueComment.IsPullRequest {
		return SuccessResponse{"Not a PR. Ignoring."}
	}
	switch {
	case isSquashCommand(issueComment.Comment):
		return handleSquashCommand(issueComment, git, pullRequests, repositories)
	case isMergeCommand(issueComment.Comment):
		return handleMergeCommand(issueComment, issues, pullRequests, repositories, git)
	case isPlusOneComment.MatchString(issueComment.Comment):
		return handlePlusOneComment(issueComment, pullRequests, repositories)
	}
	return SuccessResponse{"Not a command I understand. Ignoring."}
}

func handlePullRequestEvent(body []byte, pullRequests PullRequests, repositories Repositories) Response {
	pullRequestEvent, err := parsePullRequestEvent(body)
	if err != nil {
		return ErrorResponse{err, http.StatusInternalServerError, "Failed to parse the request's body"}
	} else if !(pullRequestEvent.Action == "opened" || pullRequestEvent.Action == "synchronize") {
		return SuccessResponse{"PR not opened or synchronized. Ignoring."}
	}
	return checkForFixupCommits(pullRequestEvent, pullRequests, repositories)
}

func initGithubClient(accessToken string) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: accessToken},
	)
	tc := oauth2.NewClient(oauth2.NoContext, ts)
	return github.NewClient(tc)
}
