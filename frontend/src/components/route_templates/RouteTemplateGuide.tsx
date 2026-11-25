"use client";

import { useState } from "react";
import { BookOpen } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useT } from "@/i18n";
import RouteTemplateJsonGuide from "./RouteTemplateJsonGuide";

/** RouteTemplateGuide 返回完整配置说明弹窗；无参数，返回指南入口与弹窗。
 * 模板库和编辑页共用；打开关闭只改变展示状态，不修改草稿或后台数据。 */
export default function RouteTemplateGuide() {
  const [open, setOpen] = useState(false);
  const t = useT();
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        <BookOpen />
        {t("pages.routeTemplates.jsonGuide.openButton")}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="flex max-h-[calc(100dvh-2rem)] min-w-0 flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
          <DialogHeader className="border-b px-5 py-4 pe-12 sm:px-6 sm:pe-14">
            <DialogTitle className="text-lg font-semibold">{t("pages.routeTemplates.jsonGuide.openButton")}</DialogTitle>
            <DialogDescription>{t("pages.routeTemplates.jsonGuide.dialogDescription")}</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 overflow-y-auto px-5 py-4 sm:px-6">
            <RouteTemplateJsonGuide />
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
