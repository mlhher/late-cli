#!/bin/sh
# Inline tool script for "greet" tool
# Receives JSON arguments object on stdin
input=$(cat)
name=$(echo "$input" | sed -n 's/.*"name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
if [ -z "$name" ]; then
  name="world"
fi
echo "Hello, $name! Greetings from example-plugin inline tool."
