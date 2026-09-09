# Welcome 💖

Before anything else, thank you for taking some of your precious time to help this project move forward. ❤️

If you're new to open source and feeling a bit nervous 😳, we understand! We recommend watching [this excellent guide](https://egghead.io/talks/git-how-to-make-your-first-open-source-contribution)
to give you a grounding in some of the basic concepts. You could also watch [this talk](https://www.youtube.com/watch?v=tuSk6dMoTIs) from our very own wonderful [Marit van Dijk](https://github.com/mlvandijk) on her experiences contributing to Cucumber.

We want you to feel safe to make mistakes, and ask questions. If anything in this guide or anywhere else in the codebase doesn't make sense to you, please let us know! It's through your feedback that we can make this codebase more welcoming, so we'll be glad to hear thoughts.

You can chat with us in the `#committers` channel in our [community Discord](https://cucumber.io/docs/community/get-in-touch/#discord), or feel free to [raise an issue] if you're experiencing any friction trying make your contribution.

## Setup

To get your development environment set up, you'll need to [install Go]. We're currently using version 1.17 for development.

Once that's done, try running the tests:

    make test

If everything passes, you're ready to hack!

[install go]: https://golang.org/doc/install
[community Discord]: https://cucumber.io/community#discord
[raise an issue]: https://github.com/cucumber/godog/issues/new/choose

## Changing dependencies

If dependencies have changed, you will also need to update the _examples module. `go mod tidy` should be sufficient.

## Common development commands

With Go and [just](https://github.com/casey/just) installed (recipes tested with
just 1.58.0), run these from the repository:

```sh
just                         # List commands; also: just help
just build                   # Build _artifacts/godog
just test                    # Root-module tests with the race detector
just test -run TestName       # Focus on a Go test
just test-examples            # Test the separate _examples module
just bdd                     # Strict Godog feature tests
just godog workflow --help   # Explore workflow commands
just check                   # Formatting, vet, both modules, and feature tests
```

`just fmt` formats Go files; `just fmt-check` checks without writing.
`just coverage` writes `_artifacts/coverage.txt`; `just coverage-html` opens it.
Extra arguments to `test`, `test-examples`, `coverage`, `bdd`, and `godog` are
forwarded literally; quote arguments containing spaces. Test recipes always
include `./...`, so use Go flags such as `-run` to filter tests.

`just check` is a local check suite. Hosted CI additionally runs Staticcheck
and platform/version jobs. Existing Make targets remain available.
Recipe contract tests run through `go test ./internal/devtools`; they skip when
just is absent. CI sets `REQUIRE_JUST=1` to require them.
