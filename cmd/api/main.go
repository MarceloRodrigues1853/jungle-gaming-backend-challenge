// Command api inicia o serviço HTTP de transações financeiras.
package main

import "github.com/MarceloRodrigues1853/jungle-gaming-backend-challenge/internal/bootstrap"

// main entrega sinais e lifecycle ao aplicativo composto com Uber Fx.
func main() {
	bootstrap.NewApp().Run()
}
