package main

import "github.com/svnbjrn/spoon/internal/forge"

// ownerEvidenceToJSON renders the owner sample behind the fork-farmer
// penalty so a reader can see what the score rested on. observedRepos
// counts the repositories that were sampled, in sampleOrder; it equals
// the owner's public repository count only when complete is true.
func ownerEvidenceToJSON(p *forge.OwnerProfile) map[string]any {
	return map[string]any{
		"login":         p.Login,
		"observedRepos": p.TotalPublicRepos,
		"forks":         p.ForkCount,
		"signalForks":   p.SignalForkCount,
		"nonForkRepos":  p.NonForkRepoCount,
		"sampleOrder":   p.SampleOrder,
		"complete":      p.Complete,
		"fetchedAt":     p.FetchedAt,
	}
}
