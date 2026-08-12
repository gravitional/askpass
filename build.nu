#!/usr/bin/env nu

const PACKAGE_PATH = "."

def main [] {
    cd $env.FILE_PWD
    $env.CGO_ENABLED = "0"

    let build_dir = "build"
    let is_windows = ($nu.os-info.name == "windows")
    mkdir $build_dir

    go fmt ./...
    if $env.LAST_EXIT_CODE != 0 {
        print --stderr "Error: go fmt failed"
        exit 1
    }

    for binary_name in ["ssh-wrapper" "scp-wrapper"] {
        let artifact_name = if $is_windows {
            $"($binary_name).exe"
        } else {
            $binary_name
        }
        let artifact_path = $"($build_dir)/($artifact_name)"

        print $"Building ($PACKAGE_PATH) -> ($artifact_path)"
        go build '-ldflags=-s -w' -o $artifact_path $PACKAGE_PATH
        if $env.LAST_EXIT_CODE != 0 {
            print --stderr $"Error: failed to build ($artifact_name)"
            exit 1
        }
    }

    let config_path = $"($build_dir)/ssh-wrapper-config.toml"
    if not ($config_path | path exists) {
        cp ssh-wrapper-config.toml $config_path
        print $"Created ($config_path) from the example config"
    }

    print $"Build completed: ($build_dir)"
}
