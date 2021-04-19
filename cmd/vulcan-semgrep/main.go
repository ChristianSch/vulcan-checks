package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/src-d/go-git.v4"
	http "gopkg.in/src-d/go-git.v4/plumbing/transport/http"

	check "github.com/adevinta/vulcan-check-sdk"
	"github.com/adevinta/vulcan-check-sdk/helpers"
	checkstate "github.com/adevinta/vulcan-check-sdk/state"
	report "github.com/adevinta/vulcan-report"
	"github.com/sirupsen/logrus"
)

const (
	DefaultDepth   = 1
	DefaultRuleset = `p/r2c-security-audit`
)

var (
	checkName = "vulcan-semgrep"
	logger    = check.NewCheckLog(checkName)

	severityMap = map[string]report.SeverityRank{
		"INFO":    report.SeverityNone,
		"WARNING": report.SeverityLow,
		"ERROR":   report.SeverityHigh,
	}
)

type options struct {
	Depth   int    `json:"string"`
	Ruleset string `json:"ruleset"`
}

func main() {
	run := func(ctx context.Context, target, assetType, optJSON string, state checkstate.State) error {
		if target == "" {
			return errors.New("check target missing")
		}

		logger = logger.WithFields(logrus.Fields{
			"target":    target,
			"assetType": assetType,
		})

		opt := options{
			Depth:   DefaultDepth,
			Ruleset: DefaultRuleset,
		}
		if optJSON != "" {
			if err := json.Unmarshal([]byte(optJSON), &opt); err != nil {
				return err
			}
		}

		logger.WithFields(logrus.Fields{"options": opt}).Debug("using options")

		// We check if the target is not the public Github.
		targetURL, err := url.Parse(target)
		if err != nil {
			return err
		}

		// TODO: Support multiple authenticated Github Enterprise instances.
		githubURL, err := url.Parse(os.Getenv("GITHUB_ENTERPRISE_ENDPOINT"))
		if err != nil {
			return err
		}

		var auth *http.BasicAuth
		if githubURL.Host != "" && targetURL.Host == githubURL.Host {
			auth = &http.BasicAuth{
				Username: "username", // Can be anything except blank.
				Password: os.Getenv("GITHUB_ENTERPRISE_TOKEN"),
			}
			logger.Debug("using credentials for GitHub")
		}

		gitCreds := &helpers.GitCreds{}
		if auth != nil {
			gitCreds.User = auth.Username
			gitCreds.Pass = auth.Password
		}
		isReachable, err := helpers.IsReachable(target, assetType, gitCreds)
		if err != nil {
			logger.Warnf("Can not check asset reachability: %v", err)
		}
		if !isReachable {
			return checkstate.ErrAssetUnreachable
		}

		repoPath := filepath.Join("/tmp", filepath.Base(targetURL.Path))
		if err := os.Mkdir(repoPath, 0755); err != nil {
			return err
		}

		logger.Debugf("cloning into %s", repoPath)

		_, err = git.PlainClone(repoPath, false, &git.CloneOptions{
			URL:   target,
			Auth:  auth,
			Depth: opt.Depth,
		})
		if err != nil {
			return err
		}

		r, err := runSemgrep(ctx, logger, opt.Ruleset, repoPath)
		if err != nil {
			return err
		}

		addVulnsToState(state, r, repoPath)

		return nil
	}

	c := check.NewCheckFromHandler(checkName, run)
	c.RunAndServe()
}

func addVulnsToState(state checkstate.State, r *SemgrepOutput, repoPath string) {
	if r == nil || len(r.Results) < 1 {
		return
	}

	vulns := make(map[string]report.Vulnerability)
	for _, result := range r.Results {
		v := vuln(result, vulns)

		score := report.ScoreSeverity(severityMap[result.Extra.Severity])
		if score > v.Score {
			v.Score = score
		}

		path := strings.TrimPrefix(result.Path, fmt.Sprintf("%s/", repoPath))
		row := map[string]string{
			"Severity":     result.Extra.Severity,
			"Path":         fmt.Sprintf("%s:%d", path, result.Start.Line),
			"Message":      result.Extra.Message,
			"Match":        result.Extra.Lines,
			"Fix":          result.Extra.Fix,
			"Semgrep Rule": result.Extra.Metadata.SourceRuleURL,
		}

		v.Resources[0].Rows = append(v.Resources[0].Rows, row)

		vulns[v.Summary] = v
	}

	for _, v := range vulns {
		// Sort rows by severity, alphabetical order of the path and message.
		sort.Slice(v.Resources[0].Rows, func(i, j int) bool {
			si := severityMap[v.Resources[0].Rows[i]["Severity"]]
			sj := severityMap[v.Resources[0].Rows[j]["Severity"]]

			switch {
			case si != sj:
				return si > sj
			case v.Resources[0].Rows[i]["Path"] != v.Resources[0].Rows[j]["Path"]:
				return v.Resources[0].Rows[i]["Path"] < v.Resources[0].Rows[j]["Path"]
			default:
				return v.Resources[0].Rows[i]["Message"] < v.Resources[0].Rows[j]["Message"]
			}
		})
		state.AddVulnerabilities(v)
	}
}

func vuln(result Result, vulns map[string]report.Vulnerability) report.Vulnerability {
	aux := strings.TrimPrefix(result.Extra.Metadata.Cwe, "CWE-")
	cweParts := strings.Split(aux, ":")
	summary := strings.TrimSpace(cweParts[1])

	v, ok := vulns[summary]
	if ok {
		return v
	}

	v.Summary = summary
	//v.Description = strings.TrimSpace(strings.Join(messageParts[1:], "."))
	//v.Details = fmt.Sprintf("Check ID: %s\n", result.CheckID)
	v.References = append(v.References, "https://semgrep.dev/")
	//v.References = append(v.References, result.Extra.Metadata.References...)
	v.Resources = []report.ResourcesGroup{
		report.ResourcesGroup{
			Name: "Ocurrences",
			Header: []string{
				"Severity",
				"Path",
				"Message",
				"Match",
				"Fix",
				"Semgrep Rule",
			},
		},
	}

	cweID, err := strconv.Atoi(cweParts[0])
	if err == nil {
		v.CWEID = uint32(cweID)
	}

	return v
}