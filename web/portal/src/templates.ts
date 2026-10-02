/**
 * Pages an owner starts from, by kind of business.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Every page builder in the field starts an owner from a template sorted by trade
 * — Tanaza's hospitality/catering/retail, IronWiFi's café and events — because a
 * blank page is the hardest thing to ask a non-technical person for. These are
 * written the way the owner would write them, in both languages the portal
 * speaks, and every one of them follows the two rules of the page:
 *
 * - **What matters goes before sign-in.** The offer, the menu, the breakfast
 *   hours. Many Android phones close the sign-in window the moment the guest is
 *   online, so the page after sign-in is a bonus, never the only place something
 *   is said.
 * - **Links go after sign-in, hidden, and empty.** A template cannot know the
 *   venue's Facebook page, and a link to an example address shown to real guests
 *   is worse than no link. The owner fills it in and switches it on.
 *
 * **Here, in the portal package, and not in the console**, because these are the
 * guest's words: they are in the guest's languages, not the operator's (`admin.test.ts`
 * refuses a Vietnamese string inline in `admin/src` outside its translation
 * catalogues), and the portal is the package whose catalogues already speak
 * Vietnamese. The console imports them;
 * nothing in the portal's own pages does, so the portal's bytes are unchanged.
 * Colours are named presets from `@foyer/contract`, never literals: a venue's colour
 * is content, and neither application's colours may be written inline.
 */
import type { PortalBlock, PortalDesign } from "@foyer/contract";
import { ACCENT_PRESETS } from "@foyer/contract";

export type TemplateId = "cafe" | "restaurant" | "hotel" | "shop" | "event" | "blank";

export interface Template {
  id: TemplateId;
  /** Builds the design, with the venue's name where the words need it. */
  build: (venue: string) => PortalDesign;
}

let counter = 0;
/** A block id: unique within a design, which is all the contract asks of it. */
export function newBlockId(kind: string): string {
  counter += 1;
  const random = Math.random().toString(36).slice(2, 8);
  return `${kind}-${random}${counter.toString(36)}`.slice(0, 40);
}

const accent = (id: string): string | null => ACCENT_PRESETS.find((p) => p.id === id)?.hex ?? null;

type Draft = Omit<PortalBlock, "id" | "visible"> & { visible?: boolean };

function blocks(list: Draft[]): PortalBlock[] {
  return list.map((b) => ({ visible: true, ...b, id: newBlockId(b.kind) }));
}

const welcomeHeadline = (venue: string) => ({
  vi: `Chào mừng đến ${venue}`.slice(0, 60),
  en: `Welcome to ${venue}`.slice(0, 60),
});

/** The two links every venue asks for, waiting for the owner's addresses. */
const afterSignIn = (review: { vi: string; en: string }): Draft[] => [
  {
    kind: "button",
    placement: "connected",
    visible: false,
    url: "",
    content: { vi: { label: review.vi }, en: { label: review.en } },
  },
  {
    kind: "social",
    placement: "connected",
    visible: false,
    links: [
      { network: "facebook", url: "" },
      { network: "zalo", url: "" },
    ],
    content: { vi: { title: "Theo dõi chúng tôi" }, en: { title: "Follow us" } },
  },
];

export const TEMPLATES: readonly Template[] = [
  {
    id: "cafe",
    build: (venue) => ({
      accent: accent("coffee"),
      headline: welcomeHeadline(venue),
      blocks: blocks([
        {
          kind: "notice",
          placement: "welcome",
          content: {
            vi: { title: "Wi-Fi miễn phí cho khách của quán" },
            en: { title: "Free Wi-Fi for our guests" },
          },
        },
        {
          kind: "offer",
          placement: "welcome",
          code: "WIFI10",
          content: {
            vi: { title: "Giảm 10% cho lần gọi món tiếp theo", body: "Đưa mã này cho thu ngân khi thanh toán." },
            en: { title: "10% off your next order", body: "Show this code at the counter when you pay." },
          },
        },
        {
          kind: "list",
          placement: "welcome",
          content: {
            vi: {
              title: "Món được yêu thích",
              items: [
                { label: "Cà phê sữa đá", value: "29.000đ" },
                { label: "Bạc xỉu", value: "32.000đ" },
                { label: "Trà đào cam sả", value: "45.000đ" },
                { label: "Bánh sừng bò", value: "35.000đ" },
              ],
            },
            en: {
              title: "Guest favourites",
              items: [
                { label: "Iced milk coffee", value: "29,000₫" },
                { label: "White coffee", value: "32,000₫" },
                { label: "Peach lemongrass tea", value: "45,000₫" },
                { label: "Croissant", value: "35,000₫" },
              ],
            },
          },
        },
        ...afterSignIn({ vi: "Đánh giá quán trên Google", en: "Review us on Google" }),
      ]),
    }),
  },
  {
    id: "restaurant",
    build: (venue) => ({
      accent: accent("red"),
      headline: welcomeHeadline(venue),
      blocks: blocks([
        {
          kind: "text",
          placement: "welcome",
          content: {
            vi: { title: "Cảm ơn quý khách đã ghé", body: "Chúc quý khách một bữa ăn ngon miệng." },
            en: { title: "Thank you for dining with us", body: "We hope you enjoy your meal." },
          },
        },
        {
          kind: "list",
          placement: "welcome",
          content: {
            vi: {
              title: "Món đặc biệt hôm nay",
              items: [
                { label: "Bún chả Hà Nội", value: "65.000đ" },
                { label: "Cá kho tộ", value: "120.000đ" },
                { label: "Canh chua cá lóc", value: "95.000đ" },
              ],
            },
            en: {
              title: "Today's specials",
              items: [
                { label: "Hanoi grilled pork with noodles", value: "65,000₫" },
                { label: "Caramelised clay-pot fish", value: "120,000₫" },
                { label: "Sweet and sour fish soup", value: "95,000₫" },
              ],
            },
          },
        },
        {
          kind: "offer",
          placement: "welcome",
          content: {
            vi: { title: "Tặng món tráng miệng", body: "Cho bàn từ 4 khách trở lên. Hỏi nhân viên phục vụ." },
            en: { title: "Free dessert", body: "For tables of four or more. Ask your server." },
          },
        },
        ...afterSignIn({ vi: "Gọi đặt bàn lần sau", en: "Call to book your next table" }),
      ]),
    }),
  },
  {
    id: "hotel",
    build: (venue) => ({
      accent: accent("slate"),
      headline: welcomeHeadline(venue),
      blocks: blocks([
        {
          kind: "list",
          placement: "welcome",
          content: {
            vi: {
              title: "Thông tin cho khách lưu trú",
              items: [
                { label: "Bữa sáng · tầng 1", value: "6:30 – 10:00" },
                { label: "Hồ bơi", value: "6:00 – 20:00" },
                { label: "Trả phòng", value: "trước 12:00" },
                { label: "Lễ tân", value: "bấm số 0" },
              ],
            },
            en: {
              title: "During your stay",
              items: [
                { label: "Breakfast · 1st floor", value: "6:30 – 10:00" },
                { label: "Swimming pool", value: "6:00 – 20:00" },
                { label: "Check-out", value: "by 12:00" },
                { label: "Reception", value: "dial 0" },
              ],
            },
          },
        },
        {
          kind: "offer",
          placement: "welcome",
          content: {
            vi: { title: "Spa giảm 15% cho khách lưu trú", body: "Đặt lịch tại quầy lễ tân." },
            en: { title: "15% off at the spa for hotel guests", body: "Book at reception." },
          },
        },
        {
          kind: "text",
          placement: "connected",
          content: {
            vi: { title: "Chúc quý khách một kỳ nghỉ thoải mái" },
            en: { title: "Enjoy your stay" },
          },
        },
        ...afterSignIn({ vi: "Xem dịch vụ của khách sạn", en: "See our hotel services" }),
      ]),
    }),
  },
  {
    id: "shop",
    build: (venue) => ({
      accent: accent("rose"),
      headline: welcomeHeadline(venue),
      blocks: blocks([
        {
          kind: "notice",
          placement: "welcome",
          content: {
            vi: { title: "Cuối tuần này: giảm đến 30%" },
            en: { title: "This weekend: up to 30% off" },
          },
        },
        {
          kind: "offer",
          placement: "welcome",
          code: "WIFI50K",
          content: {
            vi: { title: "Giảm 50.000đ cho đơn từ 500.000đ", body: "Đọc mã này khi thanh toán." },
            en: { title: "50,000₫ off orders over 500,000₫", body: "Give this code at the till." },
          },
        },
        {
          kind: "list",
          placement: "welcome",
          content: {
            vi: {
              title: "Giờ mở cửa",
              items: [
                { label: "Thứ Hai – Thứ Sáu", value: "9:00 – 21:00" },
                { label: "Thứ Bảy – Chủ Nhật", value: "8:00 – 22:00" },
              ],
            },
            en: {
              title: "Opening hours",
              items: [
                { label: "Monday – Friday", value: "9:00 – 21:00" },
                { label: "Saturday – Sunday", value: "8:00 – 22:00" },
              ],
            },
          },
        },
        ...afterSignIn({ vi: "Mua sắm trực tuyến", en: "Shop online" }),
      ]),
    }),
  },
  {
    id: "event",
    build: (venue) => ({
      accent: accent("purple"),
      headline: welcomeHeadline(venue),
      blocks: blocks([
        {
          kind: "notice",
          placement: "welcome",
          content: {
            vi: { title: "Vui lòng để điện thoại ở chế độ im lặng" },
            en: { title: "Please keep your phone on silent" },
          },
        },
        {
          kind: "list",
          placement: "welcome",
          content: {
            vi: {
              title: "Lịch trình",
              items: [
                { label: "Đón khách", value: "08:30" },
                { label: "Khai mạc", value: "09:00" },
                { label: "Nghỉ trưa", value: "12:00" },
                { label: "Hội thảo chuyên đề", value: "13:30" },
              ],
            },
            en: {
              title: "Programme",
              items: [
                { label: "Registration", value: "08:30" },
                { label: "Opening", value: "09:00" },
                { label: "Lunch", value: "12:00" },
                { label: "Workshops", value: "13:30" },
              ],
            },
          },
        },
        ...afterSignIn({ vi: "Tải tài liệu sự kiện", en: "Download the event pack" }),
      ]),
    }),
  },
  {
    id: "blank",
    build: () => ({ accent: null, headline: {}, blocks: [] }),
  },
];
