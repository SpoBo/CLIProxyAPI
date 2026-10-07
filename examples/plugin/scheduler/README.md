# Scheduler Plugin

This plugin demonstrates the CLIProxyAPI C ABI scheduler capability from Go.

It implements:

- `plugin.register`
- `plugin.reconfigure`
- `scheduler.pick`

The plugin can select a configured auth ID, delegate routing to a built-in scheduler, or reject scheduler picks.

## Configuration

Add the plugin under `plugins.configs`:

```yaml
plugins:
  enabled: true
  # Optional: make this scheduler mandatory and fail closed.
  # required-scheduler: scheduler
  configs:
    scheduler:
      enabled: true
      priority: 1
      auth_id: ""
      delegate: ""
      deny: false
```

Fields:

- `auth_id`: selects this auth ID when it appears in the scheduler candidates.
- `delegate`: delegates selection to a built-in scheduler. Supported values are `""`, `fill-first`, and `round-robin`.
- `deny`: returns a scheduler error when set to `true`.

Behavior:

- When `deny` is `true`, the plugin returns an error envelope with code `scheduler_denied`.
- When `delegate` is `fill-first` or `round-robin`, the plugin returns `DelegateBuiltin` and marks the pick as handled.
- When `delegate` is any other non-empty value, the plugin leaves the pick unhandled.
- When `delegate` is empty and `auth_id` exists in the candidates, the plugin returns that auth ID and marks the pick as handled.
- When no rule matches, the plugin leaves the pick unhandled.

## Scheduler candidate quota observations

Each `SchedulerAuthCandidate` exposes only typed routing fields (`ID`,
`Provider`, `Priority`, `Weight`, and `Status`) plus `Quota` and, when the
requested model has its own observation, `ModelQuota`. `Attributes` and
`Metadata` are currently empty. Quota observations contain only `ObservedAt`
and bounded provider quota `Signals` accepted by the host's quota observer;
the signal maps are cloned for each call. Credential tokens, email addresses,
auth filenames, raw auth attributes and metadata, and provider storage are not
included. Plugins must treat all candidate fields as read-only.

When `plugins.required-scheduler` names this plugin, it must return a valid
decision for every pick: a known candidate `AuthID`, a supported built-in
delegate, or an explicit rejection. Missing/unhandled/invalid responses, plugin
errors, and panics reject the request; native scheduler fallback is disabled.

## Build

From this directory:

```bash
cd go
go build -buildmode=c-shared -o /tmp/cliproxy-scheduler-plugin.so .
rm -f /tmp/cliproxy-scheduler-plugin.so /tmp/cliproxy-scheduler-plugin.h
```
