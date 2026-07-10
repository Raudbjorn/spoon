package forksops

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/forge"
	gh "github.com/svnbjrn/spoon/internal/github"
)

type snapshotFork struct {
	FullName string
	Stars    int
	SubForks int
	PushedAt time.Time
}

type snapshotDay struct {
	Date  string
	Forks map[string]snapshotFork // key = forge fork ID
}

type snapshotHistory struct {
	Provider string
	Owner    string
	Repo     string
	Days     []snapshotDay
}

func momentumCacheFile(provider, owner, repo string) (string, error) {
	dir, err := gh.CacheDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "fork-snapshots")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	provider = strings.ToLower(provider)
	owner = strings.ToLower(owner)
	repo = strings.ToLower(repo)
	key := strings.ToLower(provider + "/" + owner + "/" + repo)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(key)))[:12]
	filename := fmt.Sprintf("%s-%s-%s-%s.json", provider, owner, repo, hash)
	return filepath.Join(dir, filename), nil
}

func loadSnapshotHistory(provider, owner, repo string) (*snapshotHistory, error) {
	path, err := momentumCacheFile(provider, owner, repo)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &snapshotHistory{Provider: provider, Owner: owner, Repo: repo}, nil
	}
	if err != nil {
		return nil, err
	}
	var hist snapshotHistory
	if err := json.Unmarshal(b, &hist); err != nil {
		return nil, err
	}
	if hist.Provider == "" {
		hist.Provider = provider
	}
	if hist.Owner == "" {
		hist.Owner = owner
	}
	if hist.Repo == "" {
		hist.Repo = repo
	}
	return &hist, nil
}

func saveSnapshotHistory(provider, owner, repo string, hist *snapshotHistory) error {
	path, err := momentumCacheFile(provider, owner, repo)
	if err != nil {
		return err
	}
	hist.Provider = provider
	hist.Owner = owner
	hist.Repo = repo
	b, err := json.MarshalIndent(hist, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

func buildMomentumMap(now time.Time, provider, owner, repo string, forks []forge.T1Data, logger io.Writer) map[string]MomentumInfo {
	unknown := unknownMomentumMap(forks)
	today := utcDate(now)
	hist, err := loadSnapshotHistory(provider, owner, repo)
	if err != nil {
		logMomentumDebug(logger, "load", err)
		return unknown
	}

	current := snapshotDay{Date: today, Forks: make(map[string]snapshotFork, len(forks))}
	for _, fk := range forks {
		current.Forks[fk.ID] = snapshotFork{
			FullName: fk.ID,
			Stars:    fk.Stars,
			SubForks: fk.SubForkCount,
			PushedAt: fk.PushedAt,
		}
	}

	days := make([]snapshotDay, 0, len(hist.Days)+1)
	for _, day := range hist.Days {
		if day.Date == today {
			continue
		}
		if retainedSnapshotDay(day.Date, today) {
			days = append(days, day)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })

	var baseline *snapshotDay
	for i := range days {
		if days[i].Date < today {
			baseline = &days[i]
			break
		}
	}

	result := unknown
	if baseline != nil {
		observedDays := daysBetween(baseline.Date, today)
		result = make(map[string]MomentumInfo, len(forks))
		for _, fk := range forks {
			base, ok := baseline.Forks[fk.ID]
			if !ok {
				result[fk.ID] = MomentumInfo{
					Status:           MomentumNew,
					StarsDelta30d:    fk.Stars,
					SubForksDelta30d: fk.SubForkCount,
					ObservedDays:     observedDays,
				}
				continue
			}
			starsDelta := fk.Stars - base.Stars
			subForksDelta := fk.SubForkCount - base.SubForks
			status := MomentumFlat
			if starsDelta > 0 || subForksDelta > 0 {
				status = MomentumRising
			} else if starsDelta < 0 || subForksDelta < 0 {
				status = MomentumFalling
			}
			result[fk.ID] = MomentumInfo{
				Status:           status,
				StarsDelta30d:    starsDelta,
				SubForksDelta30d: subForksDelta,
				ObservedDays:     observedDays,
			}
		}
	}

	days = append(days, current)
	if len(days) > 31 {
		days = days[len(days)-31:]
	}
	hist.Days = days
	if err := saveSnapshotHistory(provider, owner, repo, hist); err != nil {
		logMomentumDebug(logger, "save", err)
		return unknown
	}
	return result
}

func unknownMomentumMap(forks []forge.T1Data) map[string]MomentumInfo {
	m := make(map[string]MomentumInfo, len(forks))
	for _, fk := range forks {
		m[fk.ID] = MomentumInfo{Status: MomentumUnknown}
	}
	return m
}

func momentumProviderKey(ctx context.Context, provider forge.Forge) string {
	auth, err := provider.Auth(ctx)
	if err != nil {
		return "other"
	}
	name := auth.Provider.String()
	if name == "unknown" {
		return "other"
	}
	return name
}

func utcDate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func retainedSnapshotDay(day, today string) bool {
	dayTime, err := time.Parse("2006-01-02", day)
	if err != nil {
		return false
	}
	todayTime, err := time.Parse("2006-01-02", today)
	if err != nil {
		return false
	}
	delta := int(todayTime.Sub(dayTime).Hours() / 24)
	return delta >= 0 && delta <= 30
}

func daysBetween(start, end string) int {
	startTime, err := time.Parse("2006-01-02", start)
	if err != nil {
		return 0
	}
	endTime, err := time.Parse("2006-01-02", end)
	if err != nil {
		return 0
	}
	return int(endTime.Sub(startTime).Hours() / 24)
}

func logMomentumDebug(logger io.Writer, op string, err error) {
	if logger == nil || logger == io.Discard || err == nil {
		return
	}
	fmt.Fprintf(logger, "[momentum] snapshot %s failed: %v\n", op, err)
}
