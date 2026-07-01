import { type ReactNode } from 'react'

export function TableWrap({ children }: { children?: ReactNode }) {
	return (
		<div className="overflow-x-auto -mx-1 px-1">
			<table>{children}</table>
		</div>
	)
}
