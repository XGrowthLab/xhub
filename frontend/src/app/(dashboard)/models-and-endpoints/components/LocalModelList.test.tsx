import { I18nProvider, LOCALE_COOKIE } from "@/i18n";
import { render, screen, fireEvent } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { LocalModelList } from "./LocalModelList";
import { priceCatalogRows } from "./priceCatalogRows";
/** 前置本地公开、私有及下架记录；验证完整列表、搜索、上下架回调和不可售禁用；DOM由测试框架清理。 */
it("本地列表保留记录并允许上下架", () => {
  const rows = priceCatalogRows({
    models: [
      { id: "a", market_catalog: true },
      { id: "b", market_catalog: true, delisted: true, private: true },
    ],
  });
  const listing = vi.fn(),
    detail = vi.fn();
  localStorage.removeItem(LOCALE_COOKIE); document.cookie=`${LOCALE_COOKIE}=;path=/;max-age=0`;
  render(<I18nProvider initialLocale="zh-CN"><LocalModelList rows={rows} onDetail={detail} onListing={listing} /></I18nProvider>);
  expect(screen.getByRole("status")).toHaveTextContent("本地共 2 个模型");
  expect(screen.getByRole("button", { name: "上架" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "下架" }));
  expect(listing).toHaveBeenCalledWith(rows[0], true);
  fireEvent.click(screen.getByRole("button", { name: "a" }));
  expect(detail).toHaveBeenCalledWith(rows[0]);
  fireEvent.change(screen.getByRole("textbox", { name: "搜索本地模型" }), { target: { value: "missing" } });
  expect(screen.getByText("没有匹配的本地模型。")).toBeVisible();
});

/** 来源下架的历史资料可搜索，不能通过上架按钮消除来源状态；英文文案完整，渲染后自动清理。 */
it("preserves delisted source records in English",()=>{
 localStorage.removeItem(LOCALE_COOKIE);document.cookie=`${LOCALE_COOKIE}=;path=/;max-age=0`;
 const rows=priceCatalogRows({models:[{id:"removed",market_catalog:true,feed_unavailable:true},{id:"active",market_catalog:true}]});
 render(<I18nProvider initialLocale="en"><LocalModelList rows={rows} onDetail={vi.fn()} onListing={vi.fn()} /></I18nProvider>);
 fireEvent.change(screen.getByRole("combobox",{name:"Local model status"}),{target:{value:"down"}});
 expect(screen.getByRole("status")).toHaveTextContent("1 of 2 archived models match");
 expect(screen.getByText("Delisted at source")).toBeVisible();expect(screen.getByRole("button",{name:"Delist"})).toBeDisabled();
});

/** 自定义模型隐藏后仍可上架；独立渲染并在测试后自动清理 DOM。 */
it("allows relisting a custom model", () => {
  localStorage.removeItem(LOCALE_COOKIE); document.cookie=`${LOCALE_COOKIE}=;path=/;max-age=0`;
  const rows = priceCatalogRows({models:[{id:"custom",delisted:true}]});
  const onListing = vi.fn();
  render(<I18nProvider initialLocale="zh-CN"><LocalModelList rows={rows} onDetail={vi.fn()} onListing={onListing} /></I18nProvider>);
  fireEvent.click(screen.getByRole("button",{name:"上架"}));
  expect(onListing).toHaveBeenCalledWith(rows[0], false);
});
