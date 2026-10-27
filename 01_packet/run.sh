#!/bin/sh
set -e
case "$0" in */*) cd "${0%/*}" ;; esac
exec ./packet "$@"
