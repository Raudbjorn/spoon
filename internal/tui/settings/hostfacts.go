package settings

import (
	"os"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/embed"
	"github.com/svnbjrn/spoon/internal/store"
)

// CollectHostFacts captures read-only host facts once at settings startup.
func CollectHostFacts() HostFacts {
	facts := HostFacts{SystemConfig: configPresence()}
	if path, err := store.DefaultPath(); err != nil {
		facts.StorePath = "unavailable: " + err.Error()
	} else {
		facts.StorePath = path
	}
	if path, err := embed.DefaultFastEmbedCacheDir(); err != nil {
		facts.CachePath = "unavailable: " + err.Error()
	} else {
		facts.CachePath = path
	}
	if home, err := os.UserHomeDir(); err != nil {
		facts.Home = "unresolvable: " + err.Error()
	} else {
		facts.Home = home
	}
	return facts
}

func configPresence() string {
	if _, err := os.Stat(config.SystemPath()); err == nil {
		return "present"
	} else if os.IsNotExist(err) {
		return "absent"
	}
	return "unreadable"
}
