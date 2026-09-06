package statusline

import (
	"testing"

	"github.com/joshsymonds/steward/internal/aliases"
)

func TestAwsBgColor(t *testing.T) {
	cases := []struct {
		env  aliases.Env
		want string
	}{
		{aliases.EnvProd, "red"},
		{aliases.EnvStaging, "peach"},
		{aliases.EnvDev, "green"},
		{aliases.EnvUnknown, "peach"},
	}
	for _, c := range cases {
		if got := awsBgColor(c.env); got != c.want {
			t.Errorf("awsBgColor(%s) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestGcloudBgColor(t *testing.T) {
	cases := []struct {
		env  aliases.Env
		want string
	}{
		{aliases.EnvProd, "pink"},
		{aliases.EnvStaging, "mauve"},
		{aliases.EnvDev, "sapphire"},
		{aliases.EnvUnknown, "lavender"},
	}
	for _, c := range cases {
		if got := gcloudBgColor(c.env); got != c.want {
			t.Errorf("gcloudBgColor(%s) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestK8sBgColor(t *testing.T) {
	cases := []struct {
		env  aliases.Env
		want string
	}{
		{aliases.EnvProd, "maroon"},
		{aliases.EnvStaging, "yellow"},
		{aliases.EnvDev, "teal"},
		{aliases.EnvUnknown, "teal"},
	}
	for _, c := range cases {
		if got := k8sBgColor(c.env); got != c.want {
			t.Errorf("k8sBgColor(%s) = %q, want %q", c.env, got, c.want)
		}
	}
}
