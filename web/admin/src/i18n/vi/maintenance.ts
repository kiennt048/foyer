/** The maintenance schedule on `/privacy`, in Vietnamese. A total translation of `../maintenance.ts`. */
import type { Translation } from "../catalogue.js";
import type { maintenance } from "../maintenance.js";

export const vi: Translation<typeof maintenance> = {
  "maintenance.title": "Lịch bảo trì",
  "maintenance.body":
    "Foyer tự dọn dẹp: phiên cổng đăng nhập và bảng điều khiển đã hết hạn, phiếu quá hạn, phiên mà điểm truy cập không bao giờ đóng, lượt quét lưu giữ và mã điện thoại cũ. Thay đổi ở đây có hiệu lực ngay, không cần khởi động lại.",
  "maintenance.interval": "Chạy mỗi",
  "maintenance.interval.hint": "Thời gian giữa hai lượt chạy. Thời hạn lưu giữ được áp dụng với sai số trong khoảng này.",
  "maintenance.stale": "Đóng phiên im lặng sau",
  "maintenance.stale.hint": (minutes: number) =>
    `Số lần cập nhật kế toán bị lỡ. Với chu kỳ báo cáo năm phút, đó là ${minutes} phút im lặng. Bảng điều khiển đánh dấu phiên mồ côi ở mức 2, nên thấp nhất là 3.`,
  "maintenance.ceiling": "Số phiên tối đa một lượt được đóng",
  "maintenance.ceiling.hint":
    "Một lượt định đóng nhiều hơn sẽ không đóng phiên nào và báo lỗi cho tới khi có người xem xét: từ đây, một sự cố mất mạng và một lỗi trông giống nhau.",
  "maintenance.batch": "Số dòng lưu giữ mỗi câu lệnh",
  "maintenance.batch.hint":
    "Nhỏ hơn thì giữ khoá ngắn hơn trên cơ sở dữ liệu bận; lớn hơn thì lượt quét đầu tiên xong sớm hơn.",
  "maintenance.default": "Đây là các giá trị mặc định.",
  "maintenance.updated": (who: string, when: string) => `Thay đổi lần cuối bởi ${who}, ${when}.`,
  "maintenance.save": "Lưu cài đặt bảo trì",
  "maintenance.saved": "Đã lưu cài đặt bảo trì.",
  "maintenance.failed": "Chưa lưu được cài đặt bảo trì",
  "maintenance.restore": "Điền các giá trị mặc định",
};
