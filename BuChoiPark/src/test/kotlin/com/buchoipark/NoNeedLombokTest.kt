package com.buchoipark

import io.kotest.core.spec.style.FreeSpec
import io.kotest.matchers.shouldBe

class NoNeedLombokTest : FreeSpec({

    "kotlin no need lombok" {
        val person = Person(
            name = "박시준",
            address = "선릉",
        )

        person.name shouldBe "박시준"
        person.address shouldBe "선릉"
    }

    "부최박 조는 모든 사람이 선릉에서 모인다." - {
        "부대권은 선릉에 모인다." {
            val person = Person(
                name = "부대권",
                address = "선릉",
            )

            person.name shouldBe "부대권"
            person.address shouldBe "선릉"
        }

        "최현구는 선릉에 모인다." {
            val person = Person(
                name = "최현구",
                address = "선릉",
            )

            person.name shouldBe "최현구"
            person.address shouldBe "선릉"
        }

        "박시준은 선릉에 모인다." {
            val person = Person(
                name = "박시준",
                address = "선릉",
            )

            person.name shouldBe "박시준"
            person.address shouldBe "선릉"
        }
    }
})

data class Person(
    val name: String,
    val address: String,
)
