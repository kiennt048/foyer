/** The settings screens' shared parts in Vietnamese. A total translation of `../settings.ts`. */
import type { Translation } from "../catalogue.js";
import type { settings } from "../settings.js";

export const vi: Translation<typeof settings> = {
  "settings.advanced.title": "Cài đặt nâng cao",
  "settings.advanced.lede":
    "Các giới hạn kỹ thuật, mỗi cái đã đặt sẵn một giá trị mặc định an toàn. Phần lớn địa điểm không bao giờ đổi; chỉ đổi khi bộ phận hỗ trợ hoặc một phép đo yêu cầu.",
  "settings.reset": "Đặt lại mặc định",
  "settings.range": (min: string, max: string, u: string) => `Từ ${min} đến ${max} ${u}.`,
  "settings.default": (d: string, u: string) => `Mặc định ${d} ${u}.`,
  "settings.rangeCount": (min: string, max: string) => `Từ ${min} đến ${max}.`,
  "settings.defaultCount": (d: string) => `Mặc định ${d}.`,
  "settings.changed": "Đã khác giá trị mặc định.",
  "settings.unit.seconds": () => "giây",
  "settings.unit.minutes": () => "phút",
  "settings.unit.kb": () => "KB",
  "settings.unit.mb": () => "MB",
};
