"""Validation rules shared by the example's two transaction scripts."""

import re

CODE_PATTERN = re.compile(r"^[A-Z0-9]{4,32}$")
GENERATED_CODE_LENGTH = 8
MAX_TEAM_ID_LENGTH = 64
MAX_USES_LIMIT = 1000
MAX_REDEEMED_BY_LENGTH = 128
