#!/bin/sh
set -eu

resolver_ip=$(awk '
  $1 == "nameserver" && $2 ~ /^[0-9.]+$/ {
    count = split($2, octets, ".")
    if (count != 4) next
    valid = 1
    for (i = 1; i <= 4; i++) {
      if (octets[i] !~ /^[0-9]+$/ || length(octets[i]) > 3 || octets[i] + 0 > 255) valid = 0
    }
    if (valid) {
      printf "%d.%d.%d.%d\n", octets[1] + 0, octets[2] + 0, octets[3] + 0, octets[4] + 0
      exit
    }
  }
' /etc/resolv.conf)

if [ -z "$resolver_ip" ]; then
  echo 'No valid IPv4 nameserver found in /etc/resolv.conf' >&2
  exit 1
fi

simulation_search_domain=$(awk '
  $1 == "search" {
    for (i = 2; i <= NF; i++) {
      candidate = tolower($i)
      if (length(candidate) > 253 || candidate !~ /^[a-z0-9-]+[.]svc[.][a-z0-9.-]+$/) continue
      count = split(candidate, labels, /[.]/)
      if (count < 4 || labels[2] != "svc") continue
      valid = 1
      for (j = 1; j <= count; j++) {
        if (length(labels[j]) == 0 || length(labels[j]) > 63 || labels[j] !~ /^[a-z0-9-]+$/ || labels[j] ~ /^-/ || labels[j] ~ /-$/) valid = 0
      }
      if (valid) {
        print candidate
        exit
      }
    }
  }
' /etc/resolv.conf)

simulation_host=simulation-controller
if [ -n "$simulation_search_domain" ]; then
  simulation_host="simulation-controller.$simulation_search_domain"
fi

{
  printf 'resolver %s valid=10s ipv6=off;\n' "$resolver_ip"
  printf 'set $simulation_controller %s:8083;\n' "$simulation_host"
} > /tmp/ottawa-nginx-resolver.conf
