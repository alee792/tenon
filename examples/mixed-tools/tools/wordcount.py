"""Authored Python tool: count whitespace-separated words and characters."""

from pydantic import BaseModel

description = "Count the whitespace-separated words and the characters in a text."


class Input(BaseModel):
    text: str


class Output(BaseModel):
    words: int
    chars: int


def execute(input: Input, context) -> Output:
    """Count words and characters in the given text.

    The host calls execute with the validated Input and a context dict
    (carrying, e.g., requestId), so execute takes two positional arguments.
    """
    return Output(words=len(input.text.split()), chars=len(input.text))
