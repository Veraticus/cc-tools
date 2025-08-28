#!/usr/bin/env bash
echo "Called with args: $@" >> /tmp/cc-tools-debug.log
echo "Number of args: $#" >> /tmp/cc-tools-debug.log
echo "First arg: $1" >> /tmp/cc-tools-debug.log
echo "All args:" >> /tmp/cc-tools-debug.log
for arg in "$@"; do
  echo "  - '$arg'" >> /tmp/cc-tools-debug.log
done
echo "---" >> /tmp/cc-tools-debug.log

# Now call the actual binary
exec /nix/store/w72lbrrf31d0hcpfakq13hsiirpccgxn-cc-tools-dirty/bin/cc-tools "$@"