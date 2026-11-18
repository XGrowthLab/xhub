import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it, vi } from "vitest";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./dropdown-menu";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./tabs";

/** 页签过渡不重置显式保留的表单；输入后切换再返回应原样保留，全部 DOM 由测试框架清理。 */
it("keeps mounted tab drafts intact while changing tabs", async () => {
  const user = userEvent.setup();
  render(
    <Tabs defaultValue="draft">
      <TabsList><TabsTrigger value="draft">Draft</TabsTrigger><TabsTrigger value="details">Details</TabsTrigger></TabsList>
      <TabsContent value="draft" keepMounted><input aria-label="Draft text" defaultValue="" /></TabsContent>
      <TabsContent value="details">Details content</TabsContent>
    </Tabs>,
  );
  await user.type(screen.getByRole("textbox", { name: "Draft text" }), "保留原始输入");
  await user.click(screen.getByRole("tab", { name: "Details" }));
  expect(screen.queryByRole("textbox", { name: "Draft text" })).not.toBeInTheDocument();
  await user.click(screen.getByRole("tab", { name: "Draft" }));
  expect(screen.getByRole("textbox", { name: "Draft text" })).toHaveValue("保留原始输入");
});

/** 普通菜单打开时不锁页面或阻止选择；无外部依赖，选择后关闭，测试卸载清理浮层。 */
it("opens nonmodal action menus without locking page scroll", async () => {
  const user = userEvent.setup();
  const select = vi.fn();
  render(
    <DropdownMenu>
      <DropdownMenuTrigger>Actions</DropdownMenuTrigger>
      <DropdownMenuContent><DropdownMenuItem onClick={select}>Inspect</DropdownMenuItem></DropdownMenuContent>
    </DropdownMenu>,
  );
  const originalOverflow = document.body.style.overflow;
  screen.getByRole("button", { name: "Actions" }).focus();
  await user.keyboard("{ArrowDown}");
  expect(document.body).toHaveStyle({ overflow: originalOverflow });
  await user.click(await screen.findByRole("menuitem", { name: "Inspect" }));
  expect(select).toHaveBeenCalledOnce();
  expect(screen.queryByRole("menuitem", { name: "Inspect" })).not.toBeInTheDocument();
});
