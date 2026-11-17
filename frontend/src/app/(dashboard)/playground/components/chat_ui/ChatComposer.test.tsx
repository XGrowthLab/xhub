import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatComposer } from "./ChatComposer";

const renderComposer = (props: Partial<React.ComponentProps<typeof ChatComposer>> = {}) =>
  render(<ChatComposer value="" onChange={vi.fn()} onSubmit={vi.fn()} placeholder="Send a message" {...props} />);

const addonOf = (container: HTMLElement) =>
  container.querySelector<HTMLElement>("[data-slot=input-group-addon]") as HTMLElement;

describe("ChatComposer", () => {
  /** 示例按钮只回填英文提示词，用户中文原文保持不变；禁用提交仍生效，DOM 自动清理。 */
  it("keeps original input while selecting an English example", () => {
    const onChange = vi.fn(), onSuggestionSelect = vi.fn(), onSubmit = vi.fn();
    renderComposer({ value: "用户自己的内容", suggestions: ["Write me a poem"], showSuggestions: true, onChange, onSuggestionSelect, onSubmit, submitDisabled: true });
    expect(screen.getByRole("textbox")).toHaveValue("用户自己的内容");
    fireEvent.click(screen.getByRole("button", { name: "Write me a poem" }));
    expect(onSuggestionSelect).toHaveBeenCalledWith("Write me a poem");
    expect(onChange).not.toHaveBeenCalled();
    expect(onSubmit).not.toHaveBeenCalled();
  });
  it("should submit on Enter", () => {
    const onSubmit = vi.fn();
    renderComposer({ onSubmit });

    fireEvent.keyDown(screen.getByTestId("chat-composer-input"), { key: "Enter" });

    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("should not submit on Shift+Enter", () => {
    const onSubmit = vi.fn();
    renderComposer({ onSubmit });

    fireEvent.keyDown(screen.getByTestId("chat-composer-input"), { key: "Enter", shiftKey: true });

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("should not submit while an IME composition is active", () => {
    const onSubmit = vi.fn();
    renderComposer({ onSubmit });

    fireEvent.keyDown(screen.getByTestId("chat-composer-input"), { key: "Enter", isComposing: true });

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("should not submit on Enter or click when submitDisabled", () => {
    const onSubmit = vi.fn();
    renderComposer({ onSubmit, submitDisabled: true });

    fireEvent.keyDown(screen.getByTestId("chat-composer-input"), { key: "Enter" });
    fireEvent.click(screen.getByTestId("chat-send-button"));

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("should submit when the send button is clicked", () => {
    const onSubmit = vi.fn();
    renderComposer({ onSubmit });

    fireEvent.click(screen.getByTestId("chat-send-button"));

    expect(onSubmit).toHaveBeenCalledTimes(1);
  });

  it("should swap send for a stop button that cancels while loading", () => {
    const onSubmit = vi.fn();
    const onCancel = vi.fn();
    renderComposer({ onSubmit, onCancel, isLoading: true });

    expect(screen.queryByTestId("chat-send-button")).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("chat-stop-button"));

    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("should render suggestions only when asked and report the chosen one", () => {
    const onSuggestionSelect = vi.fn();
    const { rerender } = renderComposer({ suggestions: ["Summarize this"], onSuggestionSelect });

    expect(screen.queryByTestId("chat-suggested-actions")).not.toBeInTheDocument();

    rerender(
      <ChatComposer
        value=""
        onChange={vi.fn()}
        onSubmit={vi.fn()}
        placeholder="Send a message"
        suggestions={["Summarize this"]}
        showSuggestions
        onSuggestionSelect={onSuggestionSelect}
      />,
    );
    fireEvent.click(screen.getByText("Summarize this"));

    expect(onSuggestionSelect).toHaveBeenCalledWith("Summarize this");
  });

  it("should not nest a form inside the composer when body renders one", () => {
    const { container } = renderComposer({
      body: (
        <form data-testid="body-form">
          <input aria-label="tool argument" />
        </form>
      ),
    });

    expect(container.querySelectorAll("form")).toHaveLength(1);
    expect(screen.getByTestId("body-form")).toBeInTheDocument();
  });

  it("should focus the message box, not a tool input, when the toolbar gap is clicked", () => {
    const { container } = renderComposer({
      tools: <input type="file" aria-label="Attach file" />,
    });

    fireEvent.click(addonOf(container));

    expect(screen.getByTestId("chat-composer-input")).toHaveFocus();
  });
});
