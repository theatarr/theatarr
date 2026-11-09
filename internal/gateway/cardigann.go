package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/theatarr/theatarr/internal/policy"
)

// CardigannDefinitionsBudget is the target key and budget of the cardigann definitions fetch.
const CardigannDefinitionsBudget = "cardigann-definitions"

// CardigannCommit asks GitHub's commits API for the definitions branch's newest commit id, like QualityCommit, on the
// definitions fetch's own budget.
func (g *Gateway) CardigannCommit(ctx context.Context, base *policy.Policy, commitURL string) (string, error) {
	res, err := g.syncGet(ctx, base, CardigannDefinitionsBudget, commitURL, 4<<10, "application/vnd.github.sha",
		&g.runtime.cardigannOnce, &g.runtime.cardigannBucket)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("gateway: definitions commit request failed")
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	sha := strings.ToLower(strings.TrimSpace(string(b)))
	if !qualityCommitRE.MatchString(sha) {
		return "", errors.New("gateway: definitions commit answer is not a commit id")
	}
	return sha, nil
}

// CardigannArchive fetches the definitions repository archive of one commit (at most maxBytes).
func (g *Gateway) CardigannArchive(ctx context.Context, base *policy.Policy, archiveURL string, maxBytes int64) ([]byte, error) {
	res, err := g.syncGet(ctx, base, CardigannDefinitionsBudget, archiveURL, maxBytes, "",
		&g.runtime.cardigannOnce, &g.runtime.cardigannBucket)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("gateway: definitions archive request failed")
	}
	return io.ReadAll(res.Body)
}
