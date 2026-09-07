#!/usr/bin/env bash
# Capture live sitemap responses as test fixtures.
#
# Publisher XML drifts, and a shape change is the failure mode that reaches
# production silently. Re-run this when adding a source or when a parse
# starts failing, and commit the result so CI catches the next drift.
#
# TestFetchAgainstRecordedFixtures serves only the captured root document
# from httptest; it does not serve any <sitemap><loc> children locally. If a
# publisher's sitemap is index-shaped, a captured fixture for it would make
# the test follow those children out to the real, live publisher. The test
# asserts every fixture's root is <urlset> specifically to catch this, so
# do not commit a <sitemapindex> fixture without also teaching the test to
# serve its children.
#
# Usage: scripts/capture-fixtures.sh [sources-file]
set -euo pipefail

SOURCES="${1:-configs/sources.example.yaml}"
OUT="internal/source/testdata/sitemaps"
UA="Mozilla/5.0 (compatible; samvad-harvester/2.0; +https://github.com/samvad-hq/samvad-news-harvester)"

mkdir -p "$OUT"

python3 - "$SOURCES" <<'PY' | while read -r id url; do
import re, sys
doc = open(sys.argv[1]).read()
for m in re.finditer(r'-\s+id:\s*(\S+)[\s\S]*?url:\s*(\S+)', doc):
    print(m.group(1), m.group(2))
PY
  printf '%-24s ' "$id"
  code=$(curl -sS -o "$OUT/$id.xml.tmp" -w '%{http_code}' -L --max-time 30 -A "$UA" "$url" || echo 000)
  if [ "$code" = "200" ] && grep -qE '<(urlset|sitemapindex)' "$OUT/$id.xml.tmp"; then
    # Keep fixtures small: the first 200 <url> records exercise every shape.
    python3 - "$OUT/$id.xml.tmp" "$OUT/$id.xml" <<'PY'
import re, sys
src, dst = sys.argv[1], sys.argv[2]
doc = open(src, encoding='utf-8', errors='replace').read()
blocks = re.findall(r'<url>[\s\S]*?</url>', doc)
if len(blocks) > 200:
    head = doc[:doc.index(blocks[0])]
    open(dst, 'w', encoding='utf-8').write(head + '\n'.join(blocks[:200]) + '\n</urlset>')
else:
    open(dst, 'w', encoding='utf-8').write(doc)
PY
    echo "captured ($(wc -c < "$OUT/$id.xml" | tr -d ' ') bytes)"
  else
    echo "skipped (status $code)"
  fi
  rm -f "$OUT/$id.xml.tmp"
done
