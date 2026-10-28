import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TextField } from "@mui/material";
import { identifierField } from "./identifierField.js";

// What is actually worth pinning here is not the string "off" -- it is
// where the attributes end up. A MUI TextField hands a prop it does not
// recognise to the FormControl <div> around the field, where an
// autocapitalize means nothing at all and where a passing render would
// still look right; inputProps is what reaches the <input> itself. The
// component tests that assert their own fields carry this (RepoField,
// NewTaskOverlay, GeminiModelFields) are asserting the same thing about
// one field each; this one says it about the mechanism.
//
// It also pins the React spelling of each attribute: autoCorrect and
// spellCheck reach the DOM as autocorrect and spellcheck only because
// React knows those props, and a rename that broke one of them would
// otherwise be invisible until somebody typed a repo name on a phone.
describe("identifierField", () => {
  it("reaches the <input> a MUI field renders, not the wrapper around it", () => {
    render(<TextField label="Base branch" inputProps={identifierField} />);
    const field = screen.getByLabelText("Base branch");
    expect(field).toHaveAttribute("autocapitalize", "off");
    expect(field).toHaveAttribute("autocorrect", "off");
    expect(field).toHaveAttribute("spellcheck", "false");
  });

  it("spreads onto a bare input", () => {
    render(<input aria-label="Repo" {...identifierField} />);
    const field = screen.getByLabelText("Repo");
    expect(field).toHaveAttribute("autocapitalize", "off");
    expect(field).toHaveAttribute("autocorrect", "off");
    expect(field).toHaveAttribute("spellcheck", "false");
  });

  it("leaves room for the field's other input props", () => {
    render(
      <TextField
        label="Author email"
        inputProps={{ ...identifierField, maxLength: 128 }}
      />,
    );
    const field = screen.getByLabelText("Author email");
    expect(field).toHaveAttribute("autocapitalize", "off");
    expect(field).toHaveAttribute("autocorrect", "off");
    expect(field).toHaveAttribute("maxlength", "128");
  });
});
