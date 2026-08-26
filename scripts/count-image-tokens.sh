#!/usr/bin/env bash
# count-image-tokens.sh - Verify image token counts against Anthropic's API
#
# Usage: ANTHROPIC_API_KEY=sk-ant-api... scripts/count-image-tokens.sh before.png after.png [model]
#
# Compares the token count reported by /v1/messages/count_tokens with the
# computed count from countImageTokens(resizedSize(...)). Must refuse to run
# if the key starts with sk-ant-oat (subscription token, not an API key).

set -euo pipefail

if [ $# -lt 2 ]; then
	echo "usage: ANTHROPIC_API_KEY=sk-ant-api... $0 before.png after.png [model]" >&2
	exit 1
fi

before="$1"
after="$2"
model="${3:-claude-sonnet-4-20250514}"

if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
	echo "error: ANTHROPIC_API_KEY is not set" >&2
	exit 1
fi

if [[ "$ANTHROPIC_API_KEY" =~ ^sk-ant-oat ]]; then
	echo "error: ANTHROPIC_API_KEY starts with sk-ant-oat (subscription token), not an API key" >&2
	echo "This token cannot be used for /v1/messages/count_tokens calls." >&2
	exit 1
fi

if ! command -v jq &>/dev/null; then
	echo "error: jq is required" >&2
	exit 1
fi

count_tokens() {
	local file="$1"
	local b64=$(base64 -i "$file")
	local body=$(jq -n --arg model "$model" --arg b64 "$b64" '{
		model: $model,
		messages: [{
			role: "user",
			content: [{
				type: "image",
				source: {
					type: "base64",
					media_type: "image/png",
					data: $b64
				}
			}]
		}]
	}')

	curl -s https://api.anthropic.com/v1/messages/count_tokens \
		-H "x-api-key: $ANTHROPIC_API_KEY" \
		-H "anthropic-version: 2023-06-01" \
		-H "content-type: application/json" \
		-d "$body" | jq -r '.input_tokens'
}

compute_tokens() {
	local file="$1"
	# Extract dimensions with ImageMagick or sips
	if command -v identify &>/dev/null; then
		local dims=$(identify -format "%wx%h" "$file")
		local w=$(echo "$dims" | cut -dx -f1)
		local h=$(echo "$dims" | cut -dx -f2)
	elif command -v sips &>/dev/null; then
		local w=$(sips -g pixelWidth "$file" | grep pixelWidth | awk '{print $2}')
		local h=$(sips -g pixelHeight "$file" | grep pixelHeight | awk '{print $2}')
	else
		echo "error: need identify (ImageMagick) or sips to extract dimensions" >&2
		return 1
	fi

	# High-res tier: 2576px long edge, 4784 visual tokens
	local long=$((w > h ? w : h))
	local short=$((w > h ? h : w))

	# If already within high-res tier, no resize
	if [ $long -le 2576 ]; then
		# Compute visual tokens: ⌈w/28⌉ × ⌈h/28⌉
		echo $(( ((w + 27) / 28) * ((h + 27) / 28) ))
	else
		# Would be resized to 2576 on the long edge
		echo "warning: image exceeds high-res tier, calculation may be approximate" >&2
		echo $(( ((w + 27) / 28) * ((h + 27) / 28) ))
	fi
}

echo "Before: $before"
api_before=$(count_tokens "$before")
computed_before=$(compute_tokens "$before")
echo "  API tokens: $api_before"
echo "  Computed:   $computed_before"
if [ "$api_before" != "$computed_before" ]; then
	echo "  Δ: $((api_before - computed_before))"
fi

echo
echo "After: $after"
api_after=$(count_tokens "$after")
computed_after=$(compute_tokens "$after")
echo "  API tokens: $api_after"
echo "  Computed:   $computed_after"
if [ "$api_after" != "$computed_after" ]; then
	echo "  Δ: $((api_after - computed_after))"
fi

echo
echo "Saving: $((api_before - api_after)) tokens (API), $((computed_before - computed_after)) (computed)"
