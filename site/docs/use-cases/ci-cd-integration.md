---
sidebar_position: 4
---

# CI/CD Integration

Integrating agnt into continuous integration and deployment pipelines.

## Overview

agnt can enhance CI/CD pipelines by:

- Detecting project types automatically
- Running builds and tests with detailed output
- Capturing debugging information on failures
- Providing consistent interface across project types

## GitHub Actions

### Basic Integration

```yaml
name: CI

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.24'

      - name: Install agnt
        run: go install github.com/standardbeagle/agnt@latest

      - name: Detect Project
        id: detect
        run: |
          DETECT_OUTPUT=$(agnt detect)
          echo "type=$(echo $DETECT_OUTPUT | jq -r '.type')" >> $GITHUB_OUTPUT
          echo "scripts=$(echo $DETECT_OUTPUT | jq -r '.scripts | join(",")')" >> $GITHUB_OUTPUT

      - name: Install Dependencies
        run: |
          case "${{ steps.detect.outputs.type }}" in
            node) npm ci ;;
            go) go mod download ;;
            python) pip install -r requirements.txt ;;
          esac

      - name: Run Tests
        run: agnt run --script test --mode foreground-raw
```

### Matrix Testing

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        package: [core, api, web]

    steps:
      - uses: actions/checkout@v4

      - name: Test Package
        run: |
          agnt run \
            --script test \
            --path ./packages/${{ matrix.package }} \
            --id test-${{ matrix.package }} \
            --mode foreground
```

### With Debugging on Failure

```yaml
      - name: Run Tests
        id: test
        continue-on-error: true
        run: agnt run --script test --mode foreground

      - name: Debug on Failure
        if: steps.test.outcome == 'failure'
        run: |
          # Get test output
          agnt proc --action output --process-id test --grep "FAIL"

          # Get detailed failures
          agnt proc --action output --process-id test --tail 100

      - name: Fail if Tests Failed
        if: steps.test.outcome == 'failure'
        run: exit 1
```

## GitLab CI

### Basic Pipeline

```yaml
stages:
  - detect
  - test
  - build

detect:
  stage: detect
  script:
    - go install github.com/standardbeagle/agnt@latest
    - agnt detect > detect.json
  artifacts:
    paths:
      - detect.json

test:
  stage: test
  script:
    - agnt run --script test --mode foreground-raw
  dependencies:
    - detect

build:
  stage: build
  script:
    - agnt run --script build --mode foreground
  artifacts:
    paths:
      - dist/
```

## Jenkins Pipeline

```groovy
pipeline {
    agent any

    stages {
        stage('Install') {
            steps {
                sh 'go install github.com/standardbeagle/agnt@latest'
            }
        }

        stage('Detect') {
            steps {
                script {
                    def detect = sh(
                        script: 'agnt detect',
                        returnStdout: true
                    ).trim()
                    env.PROJECT_TYPE = readJSON(text: detect).type
                }
            }
        }

        stage('Test') {
            steps {
                sh 'agnt run --script test --mode foreground'
            }
        }

        stage('Build') {
            steps {
                sh 'agnt run --script build --mode foreground'
            }
        }
    }

    post {
        failure {
            sh 'agnt proc --action output --process-id test --grep FAIL'
        }
    }
}
```

## Monorepo Support

### Detect and Test Each Package

```yaml
name: Monorepo CI

on: [push, pull_request]

jobs:
  detect-packages:
    runs-on: ubuntu-latest
    outputs:
      packages: ${{ steps.find.outputs.packages }}
    steps:
      - uses: actions/checkout@v4
      - id: find
        run: |
          PACKAGES=$(ls -d packages/*/ | jq -R -s -c 'split("\n")[:-1]')
          echo "packages=$PACKAGES" >> $GITHUB_OUTPUT

  test-packages:
    needs: detect-packages
    runs-on: ubuntu-latest
    strategy:
      matrix:
        package: ${{ fromJson(needs.detect-packages.outputs.packages) }}
    steps:
      - uses: actions/checkout@v4

      - name: Detect Package Type
        id: detect
        run: |
          cd ${{ matrix.package }}
          agnt detect

      - name: Test Package
        run: |
          agnt run \
            --script test \
            --path ${{ matrix.package }} \
            --mode foreground-raw
```

## Port Cleanup in CI

```yaml
      - name: Clean Up Ports
        run: |
          # Ensure ports are available before starting services
          agnt proc --action cleanup_port --port 3000
          agnt proc --action cleanup_port --port 8080
```

## E2E Testing with Proxy

```yaml
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Start App
        run: |
          agnt run --script dev --id app &
          sleep 10  # Wait for app to start

      - name: Start Proxy
        run: |
          agnt proxy \
            --action start \
            --id e2e \
            --target-url http://localhost:3000 \
            --port 8080 &

      - name: Run E2E Tests
        run: |
          npx playwright test --base-url http://localhost:8080

      - name: Collect Logs on Failure
        if: failure()
        run: |
          # Get captured traffic
          agnt proxylog --proxy-id e2e --types http,error

          # Get app output
          agnt proc --action output --process-id app --tail 100
```

## Parallel Testing

```yaml
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Run All Tests in Parallel
        run: |
          # Start all tests
          agnt run --script test:unit --id unit &
          agnt run --script test:integration --id integration &
          agnt run --script test:e2e --id e2e &

          # Wait and collect results
          wait

          # Check results
          agnt proc --action list
```

## Build Matrix

```yaml
  build:
    strategy:
      matrix:
        include:
          - script: build:dev
            env: development
          - script: build:staging
            env: staging
          - script: build:prod
            env: production

    steps:
      - name: Build for ${{ matrix.env }}
        run: |
          agnt run \
            --script ${{ matrix.script }} \
            --id build-${{ matrix.env }} \
            --mode foreground
```

## Caching

```yaml
      - name: Cache Go modules
        uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: go-${{ hashFiles('**/go.sum') }}

      - name: Cache agnt
        uses: actions/cache@v4
        with:
          path: ~/go/bin/agnt
          key: agnt-${{ runner.os }}
```

## Notifications

```yaml
      - name: Notify on Failure
        if: failure()
        run: |
          FAILURES=$(agnt proc --action output --process-id test --grep FAIL | head -20)
          curl -X POST $SLACK_WEBHOOK \
            -H 'Content-Type: application/json' \
            -d "{\"text\": \"Test failures:\n$FAILURES\"}"
```

## Best Practices

1. **Use foreground mode** - Ensures proper exit codes
2. **Capture output on failure** - Debug faster
3. **Clean up ports** - Avoid conflicts in shared runners
4. **Cache agnt** - Faster CI runs
5. **Use unique IDs** - Parallel job clarity
6. **Leverage detection** - Consistent cross-project scripts

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Test/build failure |
| 2 | Configuration error |
| 3 | Process error |

## See Also

- [Process Management](/features/process-management)
- [Automated Testing](/use-cases/automated-testing)
