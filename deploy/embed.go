// Package deploy embeds the systemd unit and environment template, so the
// binary can write the ones matching its own version ("gpanel
// -print-systemd-unit"); the installer uses this on install and update.
package deploy

import _ "embed"

//go:embed gpanel.service
var SystemdUnit string

//go:embed gpanel.env
var EnvTemplate string
