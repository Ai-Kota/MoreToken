# Release Checklist

Before creating a new release, ensure all items are complete.

## Pre-Release

- [ ] All tests pass: `go test ./...`
- [ ] Build succeeds: `go build -o bin/moretoken .`
- [ ] No unresolved keys in config (or document known gaps)
- [ ] README is up to date
- [ ] CHANGELOG.md updated with new features/fixes

## Release Process

1. Tag the release:
   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```

2. Create GitHub Release:
   - Go to https://github.com/Ai-Kota/MoreToken/releases/new
   - Select the tag
   - Write release notes (see template below)
   - Attach binaries for Windows/Linux/macOS (optional)
   - Click "Publish release"

## Release Notes Template

```markdown
## What's New

### Features
- ...

### Improvements
- ...

### Bug Fixes
- ...

## Upgrade Notes

- ...
```

## Post-Release

- [ ] Announce on social media (Twitter/X, Jike, etc.)
- [ ] Update issue templates if needed
- [ ] Monitor for any immediate issues
