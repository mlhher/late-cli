#!/bin/sh
# Command handler script for /echo-cmd
# Receives JSON array of arguments on stdin
sleep 2
args=$(cat)
echo "echo-cmd received arguments: $args"
