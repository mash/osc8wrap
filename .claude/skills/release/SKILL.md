---
name: release
description: Use when asked to release or create a new version of osc8wrap
---

# Release

## Overview

Create a release for osc8wrap.

## Workflow

1. **Check version**: If user did not specify a version, use AskUserQuestion to ask

2. **Check existing versions**: Run `git tag --sort=-v:refname | head -5` to get recent tags and validate:
   - Follows semantic versioning (vX.Y.Z)
   - Specified version is greater than the latest
   - Version increment is reasonable (e.g., v1.0.0 → v1.0.1 or v1.1.0 or v2.0.0)

3. **Confirm inconsistencies**: If version seems inconsistent (e.g., v1.0.0 → v3.0.0), ask user to confirm

4. **Execute release**:
   ```bash
   make release VERSION=vX.Y.Z
   ```

## Notes

- Version must have `v` prefix (e.g., v1.0.0)
- Semantic versioning: MAJOR.MINOR.PATCH
