export function InlineNotice(props: { message: string }) {
  return (
    <div class="notice" role="status">
      <strong>Heads up</strong> {props.message}
    </div>
  );
}
