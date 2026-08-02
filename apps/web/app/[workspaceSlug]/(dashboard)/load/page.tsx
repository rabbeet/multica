import { LoadPage, previewLoadSnapshot } from "@multica/views/load";

export default function LoadRoute() {
  return <LoadPage snapshot={previewLoadSnapshot} preview />;
}
