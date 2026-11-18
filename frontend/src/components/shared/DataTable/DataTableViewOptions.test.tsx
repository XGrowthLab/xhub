import type { ColumnDef } from "@tanstack/react-table";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, it } from "vitest";
import { setActiveLocale, translate } from "@/i18n";
import { DataTable } from "./DataTable";
import { DataTableViewOptions } from "./DataTableViewOptions";

const columns: ColumnDef<{ name: string; id: string }>[] = [
  { accessorKey: "name", header: "Name", enableHiding: false },
  { accessorKey: "id", header: "ID" },
];

/** 构造可隐藏列的真实表格；hidden=false 提供无可隐藏列边界，无网络请求，状态随测试卸载清理。 */
function TableFixture({ hideable = true }: { hideable?: boolean }) {
  return (
    <DataTable
      columns={hideable ? columns : columns.slice(0, 1)}
      data={[{ name: "Alice", id: "sample-id" }]}
      toolbar={(table) => <DataTableViewOptions table={table} label="Columns" />}
    />
  );
}

/** 前置可隐藏 ID 列；验证多选菜单保留文档滚动样式、切换列后不关闭、Escape 可退出；自动卸载清理。 */
it.each(["en", "zh-CN"] as const)("changes visible columns without locking page scrolling in %s", async (locale) => {
  setActiveLocale(locale);
  const user = userEvent.setup();
  render(<TableFixture />);
  const bodyStyle = {
    overflow: getComputedStyle(document.body).overflow,
    paddingRight: getComputedStyle(document.body).paddingRight,
  };
  const htmlStyle = {
    overflow: getComputedStyle(document.documentElement).overflow,
    paddingRight: getComputedStyle(document.documentElement).paddingRight,
  };
  screen.getByRole("button", { name: "Columns" }).focus();
  await user.keyboard("{ArrowDown}");
  const menu = await screen.findByRole("menu", { name: translate(locale, "consoleDetails.columnsTitle") });
  expect(menu).toBeVisible();
  expect(document.body).toHaveStyle(bodyStyle);
  expect(document.documentElement).toHaveStyle(htmlStyle);
  await user.click(screen.getByRole("menuitemcheckbox", { name: "ID" }));
  expect(screen.queryByRole("cell", { name: "sample-id" })).not.toBeInTheDocument();
  expect(menu).toBeVisible();
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
});

/** 前置无可隐藏列；验证不显示无效列选择按钮，自动卸载，无外部数据。 */
it("omits the menu when no column can be hidden", () => {
  render(<TableFixture hideable={false} />);
  expect(screen.queryByRole("button", { name: "Columns" })).not.toBeInTheDocument();
});
