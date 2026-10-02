/** The venue's time zone in Vietnamese. A total translation of `../sitetime.ts`. */
import type { Translation } from "../catalogue.js";
import type { siteTime } from "../sitetime.js";

export const vi: Translation<typeof siteTime> = {
  "siteTime.title": "Múi giờ",
  "siteTime.lede":
    "Nơi ngày của địa điểm này bắt đầu. Nó quyết định khi nào giới hạn tin nhắn mỗi ngày được đặt lại, thế nào là hôm nay trên trang tổng quan và trong báo cáo, và khi nào ngày bắt đầu, ngày kết thúc của một khối nội dung có hiệu lực.",
  "siteTime.label": "Múi giờ",
  "siteTime.now": (time: string) => `Bây giờ ở đó là ${time}.`,
  "siteTime.save": "Lưu múi giờ",
  "siteTime.saved": "Đã lưu múi giờ",
  "siteTime.saveFailed": "Chưa lưu được múi giờ",
};
